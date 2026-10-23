package workbuddy

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/576469377/Agent2API/internal/llm"
)

const imagePresignPath = "/console/as/support/presigned_url"
const imageReferenceMinTTL = 31 * time.Minute

func imageAccountScope(base string, cred Credential) string {
	identity := cred.UID
	if identity == "" {
		identity = cred.AccessToken
	}
	sum := sha256.Sum256([]byte(base + "\x00" + cred.Domain + "\x00" + identity))
	return hex.EncodeToString(sum[:])
}

func newAccountImageCache(base string, cred Credential) *imageReferenceCache {
	scope := imageAccountScope(base, cred)
	dir, err := os.UserCacheDir()
	file := ""
	if err == nil {
		file = filepath.Join(dir, "Agent2API", "workbuddy-images", scope+".json")
	}
	return newImageReferenceCache(scope, file)
}

func imageURLExpiry(value string) (time.Time, bool) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") {
		return time.Time{}, false
	}
	host := strings.ToLower(u.Hostname())
	if !strings.HasSuffix(host, ".myqcloud.com") && !strings.HasSuffix(host, ".tencentcos.cn") {
		return time.Time{}, false
	}
	if u.Query().Get("q-signature") == "" {
		return time.Time{}, false
	}
	_, end, ok := strings.Cut(u.Query().Get("q-sign-time"), ";")
	if !ok {
		return time.Time{}, false
	}
	seconds, err := strconv.ParseInt(end, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(seconds, 0), true
}

type imageCandidate struct {
	key, source, mime, encoded string
	locations                  []*imageURLRef
	signed                     *imageSignedTarget
	signErr                    error
}

// prepareImageReferences keeps every message, image occurrence and original
// pixel. Only the data-URL transport representation changes. Other models stay
// inline until their remote-image support has been verified independently.
func (a *Adapter) prepareImageReferences(ctx context.Context, body upstreamRequest) (upstreamRequest, error) {
	cache := a.imageRefs
	if cache == nil || a.cfg.Platform != GlobalPlatform || body.Model != "deepseek-v4.1-flash" || cache.scope != imageAccountScope(a.cli.baseURL, a.auth.Credential()) {
		return body, nil
	}
	candidates := map[string]*imageCandidate{}
	total := 0
	for _, message := range body.Messages {
		parts, ok := message.Content.([]contentPart)
		if !ok {
			continue
		}
		for _, part := range parts {
			if part.Type != "image_url" || part.ImageURL == nil {
				continue
			}
			source := part.ImageURL.URL
			header, encoded, ok := strings.Cut(source, ",")
			if !ok || !strings.HasPrefix(header, "data:image/") || !strings.HasSuffix(header, ";base64") || len(encoded) < ((8<<10)*4/3) {
				continue
			}
			mime := strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
			if mime != "image/png" && mime != "image/jpeg" && mime != "image/gif" && mime != "image/webp" {
				continue
			}
			digest := sha256.Sum256([]byte(source))
			key := hex.EncodeToString(digest[:])
			candidate := candidates[key]
			if candidate == nil {
				candidate = &imageCandidate{key: key, source: source, mime: mime, encoded: encoded}
				candidates[key] = candidate
			}
			candidate.locations = append(candidate.locations, part.ImageURL)
			total++
		}
	}
	if total == 0 {
		return body, nil
	}
	llm.BeginImagePreparation(ctx, total)
	// Request links in bounded batches. This avoids two network round trips
	// per picture on the first turn; singleflight below still owns the PUT.
	keys := []string{}
	byObject := map[string]*imageCandidate{}
	cache.mu.Lock()
	if !time.Now().Before(cache.blockedUntil) {
		for key, candidate := range candidates {
			if cache.entries[key].URL == "" && cache.flights[key] == nil {
				object := fmt.Sprintf("uploads/%d-%s-agent2api", time.Now().UnixMilli(), randomHex(24))
				keys = append(keys, object)
				byObject[object] = candidate
			}
		}
	}
	cache.mu.Unlock()
	for offset := 0; offset < len(keys); offset += 32 {
		batch := keys[offset:min(offset+32, len(keys))]
		links, err := a.signImageObjects(ctx, batch)
		if err != nil {
			for _, object := range keys[offset:] {
				byObject[object].signErr = err
			}
			break
		}
		for object, link := range links {
			if candidate := byObject[object]; candidate != nil {
				copy := link
				candidate.signed = &copy
			}
		}
	}
	jobs := make(chan *imageCandidate, len(candidates))
	for _, candidate := range candidates {
		jobs <- candidate
	}
	close(jobs)
	var workers sync.WaitGroup
	for i := 0; i < min(4, len(candidates)); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for candidate := range jobs {
				if ctx.Err() != nil {
					return
				}
				ref, reused, uploaded, err := cache.get(ctx, candidate.key, func(shared context.Context, old imageReference) (imageReference, int64, error) {
					return a.storeImageReference(shared, candidate, old)
				})
				count := len(candidate.locations)
				if err != nil {
					llm.ObserveImagePreparation(ctx, 0, 0, 0, 0, count)
					continue
				}
				for _, location := range candidate.locations {
					location.URL = ref.URL
				}
				reusedCount := count - 1
				if reused {
					reusedCount = count
				}
				llm.ObserveImagePreparation(ctx, count, reusedCount, uploaded, int64(len(candidate.source)-len(ref.URL))*int64(count), 0)
			}
		}()
	}
	workers.Wait()
	if ctx.Err() != nil {
		return body, context.Cause(ctx)
	}
	return body, nil
}

type imageSignedTarget struct {
	ObjectKey   string `json:"object_key"`
	UploadURL   string `json:"upload_url"`
	DownloadURL string `json:"download_url"`
}

type imagePresignResponse struct {
	Code int `json:"code"`
	Data struct {
		Items []imageSignedTarget `json:"items"`
	} `json:"data"`
}

func (a *Adapter) signImageObjects(ctx context.Context, keys []string) (map[string]imageSignedTarget, error) {
	var response imagePresignResponse
	signing, cancel := context.WithTimeout(ctx, a.cfg.RequestTimeout)
	defer cancel()
	if err := a.cli.doJSON(signing, http.MethodPost, imagePresignPath, map[string]any{"object_keys": keys}, a.auth.BuildHeaders(headerModeFull), &response); err != nil {
		return nil, err
	}
	if response.Code != 0 || len(response.Data.Items) != len(keys) {
		return nil, errors.New("image reference signing unavailable")
	}
	links := map[string]imageSignedTarget{}
	for _, item := range response.Data.Items {
		up, upErr := url.Parse(item.UploadURL)
		down, downErr := url.Parse(item.DownloadURL)
		if upErr != nil || downErr != nil || up.Path != down.Path {
			continue
		}
		for _, key := range keys {
			// Do not depend on response order: a mismatched image reference
			// would silently replace historical content with another picture.
			if strings.HasSuffix(up.Path, "/"+key) {
				item.ObjectKey = key
				links[key] = item
				break
			}
		}
	}
	if len(links) != len(keys) {
		return nil, errors.New("image reference signing response did not match requested objects")
	}
	return links, nil
}

func (a *Adapter) storeImageReference(ctx context.Context, candidate *imageCandidate, old imageReference) (imageReference, int64, error) {
	if candidate.signErr != nil {
		return imageReference{}, 0, candidate.signErr
	}
	// Optional file preparation must fail back to the original inline request
	// promptly if storage is unavailable, rather than consuming the route budget.
	bounded, cancelUpload := context.WithTimeout(ctx, a.cfg.RequestTimeout)
	defer cancelUpload()
	ctx = bounded
	if old.URL != "" && time.Until(old.ExpiresAt) > imageReferenceMinTTL && a.imageObjectExists(ctx, old) {
		old.VerifiedAt = time.Now()
		return old, 0, nil
	}
	objectKey := old.ObjectKey
	if objectKey == "" {
		objectKey = fmt.Sprintf("uploads/%d-%s-agent2api", time.Now().UnixMilli(), randomHex(24))
	}
	var item imageSignedTarget
	if old.ObjectKey == "" && candidate.signed != nil {
		item = *candidate.signed
		objectKey = item.ObjectKey
	} else {
		links, err := a.signImageObjects(ctx, []string{objectKey})
		if err != nil {
			return imageReference{}, 0, err
		}
		item = links[objectKey]
	}
	expiry, valid := imageURLExpiry(item.DownloadURL)
	if _, ok := imageURLExpiry(item.UploadURL); !ok || !valid || time.Until(expiry) < imageReferenceMinTTL {
		return imageReference{}, 0, errors.New("invalid image reference target")
	}
	// Refresh an expiring signature without uploading the same object again.
	refreshed := imageReference{ObjectKey: objectKey, URL: item.DownloadURL, MD5: old.MD5, ExpiresAt: expiry, VerifiedAt: time.Now()}
	if old.ObjectKey != "" && a.imageObjectExists(ctx, refreshed) {
		return refreshed, 0, nil
	}
	raw, err := base64.StdEncoding.DecodeString(candidate.encoded)
	if err != nil {
		return imageReference{}, 0, errors.New("invalid inline image")
	}
	defer clear(raw)
	checksum := md5.Sum(raw)
	guard := newUploadGuard(ctx, a.cfg.StreamIdleTimeout)
	traced := httptrace.WithClientTrace(guard.ctx, &httptrace.ClientTrace{GotConn: func(httptrace.GotConnInfo) { guard.start() }, WroteHeaders: guard.start, WroteRequest: func(httptrace.WroteRequestInfo) { guard.finishUpload() }})
	request, err := http.NewRequestWithContext(traced, http.MethodPut, item.UploadURL, &uploadReader{Reader: bytes.NewReader(raw), guard: guard})
	if err != nil {
		guard.close()
		return imageReference{}, 0, errors.New("image upload request unavailable")
	}
	request.ContentLength = int64(len(raw))
	request.Header.Set("Content-Type", candidate.mime)
	request.Header.Set("Content-MD5", base64.StdEncoding.EncodeToString(checksum[:]))
	// Signed storage requests never receive the account's Authorization headers.
	client := *a.cli.streamHTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(request)
	if err != nil {
		guard.close()
		return imageReference{}, 0, errors.New("image upload transport failed")
	}
	io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
	res.Body.Close()
	guard.close()
	if res.StatusCode != http.StatusOK {
		return imageReference{}, 0, errors.New("image upload rejected")
	}
	refreshed.MD5 = hex.EncodeToString(checksum[:])
	return refreshed, int64(len(raw)), nil
}

func (a *Adapter) imageObjectExists(ctx context.Context, ref imageReference) bool {
	check, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(check, http.MethodGet, ref.URL, nil)
	if err != nil {
		return false
	}
	// A GET signature is not valid for HEAD. A one-byte range checks availability
	// and the original MD5 without transferring the full historical picture.
	request.Header.Set("Range", "bytes=0-0")
	client := *a.cli.streamHTTP
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := client.Do(request)
	if err != nil {
		return false
	}
	defer res.Body.Close()
	io.Copy(io.Discard, io.LimitReader(res.Body, 1))
	return (res.StatusCode == http.StatusOK || res.StatusCode == http.StatusPartialContent) && ref.MD5 != "" && strings.EqualFold(strings.Trim(res.Header.Get("ETag"), "\""), ref.MD5)
}
