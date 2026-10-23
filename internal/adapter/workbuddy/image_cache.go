package workbuddy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const maxImageReferences = 4096

type imageReference struct {
	ObjectKey  string    `json:"object_key"`
	URL        string    `json:"url"`
	MD5        string    `json:"md5"`
	ExpiresAt  time.Time `json:"expires_at"`
	VerifiedAt time.Time `json:"verified_at"`
}

type imageFlight struct {
	done     chan struct{}
	cancel   context.CancelFunc
	waiters  int
	finished bool
	ref      imageReference
	uploaded int64
	err      error
}

// imageReferenceCache belongs to a single account and origin. It stores only
// references to original files in WorkBuddy's private storage, never image bytes.
type imageReferenceCache struct {
	scope, file  string
	mu           sync.Mutex
	entries      map[string]imageReference
	flights      map[string]*imageFlight
	slots        chan struct{}
	blockedUntil time.Time
	saveTimer    *time.Timer
	storeMu      sync.Mutex
}

func newImageReferenceCache(scope, file string) *imageReferenceCache {
	c := &imageReferenceCache{scope: scope, file: file, entries: map[string]imageReference{}, flights: map[string]*imageFlight{}, slots: make(chan struct{}, 4)}
	c.load()
	return c
}

func (c *imageReferenceCache) get(ctx context.Context, key string, loader func(context.Context, imageReference) (imageReference, int64, error)) (imageReference, bool, int64, error) {
	if err := ctx.Err(); err != nil {
		return imageReference{}, false, 0, context.Cause(ctx)
	}
	c.mu.Lock()
	old := c.entries[key]
	if old.URL != "" && time.Until(old.ExpiresAt) > imageReferenceMinTTL && time.Since(old.VerifiedAt) < 2*time.Minute {
		c.mu.Unlock()
		return old, true, 0, nil
	}
	if time.Now().Before(c.blockedUntil) {
		c.mu.Unlock()
		return imageReference{}, false, 0, errors.New("image reference upload temporarily unavailable")
	}
	f, joined := c.flights[key]
	if !joined {
		shared, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		f = &imageFlight{done: make(chan struct{}), cancel: cancel}
		c.flights[key] = f
		go func() {
			var ref imageReference
			var uploaded int64
			var err error
			select {
			case c.slots <- struct{}{}:
				ref, uploaded, err = loader(shared, old)
				<-c.slots
			case <-shared.Done():
				err = context.Cause(shared)
			}
			c.mu.Lock()
			f.ref, f.uploaded, f.err, f.finished = ref, uploaded, err, true
			if c.flights[key] == f {
				delete(c.flights, key)
				if err == nil && shared.Err() == nil {
					c.entries[key] = ref
					c.pruneLocked()
					if c.file != "" && c.saveTimer == nil {
						c.saveTimer = time.AfterFunc(200*time.Millisecond, c.save)
					}
				} else if err != nil && shared.Err() == nil {
					// This only disables the optional optimization; generation still uses
					// the original inline images and account/model health is unaffected.
					c.blockedUntil = time.Now().Add(30 * time.Second)
				}
			}
			close(f.done)
			c.mu.Unlock()
			cancel()
		}()
	}
	f.waiters++
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		f.waiters--
		if f.waiters == 0 && !f.finished {
			if c.flights[key] == f {
				delete(c.flights, key)
			}
			f.cancel()
		}
	}()
	select {
	case <-ctx.Done():
		return imageReference{}, false, 0, context.Cause(ctx)
	case <-f.done:
		if ctx.Err() != nil {
			return imageReference{}, false, 0, context.Cause(ctx)
		}
		uploaded := f.uploaded
		if joined {
			uploaded = 0
		}
		return f.ref, joined || f.uploaded == 0, uploaded, f.err
	}
}

func (c *imageReferenceCache) pruneLocked() {
	for len(c.entries) > maxImageReferences {
		var oldest string
		var stamp time.Time
		for key, entry := range c.entries {
			if oldest == "" || entry.VerifiedAt.Before(stamp) {
				oldest, stamp = key, entry.VerifiedAt
			}
		}
		delete(c.entries, oldest)
	}
}

type imageReferenceFile struct {
	Version int                       `json:"version"`
	Scope   string                    `json:"scope"`
	Entries map[string]imageReference `json:"entries"`
}

func (c *imageReferenceCache) load() {
	if c.file == "" {
		return
	}
	f, err := os.Open(c.file)
	if err != nil {
		return
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 8<<20+1))
	if err != nil || len(raw) > 8<<20 {
		return
	}
	var data imageReferenceFile
	if json.Unmarshal(raw, &data) != nil || data.Version != 1 || data.Scope != c.scope {
		return
	}
	for key, entry := range data.Entries {
		expiry, ok := imageURLExpiry(entry.URL)
		if len(key) == 64 && ok && expiry.Equal(entry.ExpiresAt) && entry.ObjectKey != "" && len(entry.MD5) == 32 {
			// Always check a persisted object's existence before using it after restart.
			entry.VerifiedAt = time.Time{}
			c.entries[key] = entry
		}
	}
	c.pruneLocked()
}

func (c *imageReferenceCache) save() {
	if c.file == "" {
		return
	}
	c.storeMu.Lock()
	defer c.storeMu.Unlock()
	c.mu.Lock()
	if c.saveTimer != nil {
		c.saveTimer.Stop()
		c.saveTimer = nil
	}
	entries := make(map[string]imageReference, len(c.entries))
	for k, v := range c.entries {
		entries[k] = v
	}
	c.mu.Unlock()
	raw, err := json.Marshal(imageReferenceFile{Version: 1, Scope: c.scope, Entries: entries})
	if err != nil {
		return
	}
	dir := filepath.Dir(c.file)
	if os.MkdirAll(dir, 0700) != nil {
		return
	}
	temp, err := os.CreateTemp(dir, ".image-cache-*")
	if err != nil {
		return
	}
	name := temp.Name()
	defer os.Remove(name)
	if temp.Chmod(0600) != nil {
		temp.Close()
		return
	}
	if _, err = temp.Write(raw); err != nil {
		temp.Close()
		return
	}
	if temp.Close() != nil {
		return
	}
	_ = os.Rename(name, c.file)
}
