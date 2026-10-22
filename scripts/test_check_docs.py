import importlib.util
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("check_docs", Path(__file__).with_name("check-docs.py"))
docs = importlib.util.module_from_spec(spec)
spec.loader.exec_module(docs)


class DocumentationTests(unittest.TestCase):
    def test_chinese_code_and_duplicate_headings(self):
        text = "# 配置（gateway）\n## `API` 参考\n## Repeat\n## Repeat\n"
        self.assertEqual(docs.anchors(text), {"配置gateway", "api-参考", "repeat", "repeat-1"})

    def test_fenced_examples_and_comments_are_not_document_links(self):
        text = "# Actual\n```md\n# Example\n[broken](missing.md)\n```\n<!-- [ignored](no.md) -->\n[ok](#actual)\n"
        lines, unfinished = docs.visible_lines(text)
        self.assertIsNone(unfinished)
        self.assertNotIn("example", docs.anchors(text))
        self.assertEqual([target for _, line in lines for target in docs.links_in_line(line)], ["#actual"])
        self.assertEqual(list(docs.links_in_line("`[example](missing)` [real](target.md)")), ["target.md"])
        self.assertNotIn("fake", docs.anchors('```html\n<a id="fake"></a>\n```\n'))

    def test_links_anchors_and_fences_are_checked_offline(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "docs").mkdir()
            (root / "docs" / "usage.md").write_text("# 指标解释\n## Cache\n", encoding="utf-8")
            (root / "README.md").write_text("# Start\n[usage](docs/usage.md#指标解释)\n[web](https://example.org/not-checked)\n", encoding="utf-8")
            files, links, findings = docs.check(root)
            self.assertEqual((len(files), links, findings), (2, 1, []))
            (root / "README.md").write_text("# Start\n[bad](docs/absent.md)\n[anchor](docs/usage.md#absent)\n[escape](../outside.md)\n```sh\n", encoding="utf-8")
            _, _, findings = docs.check(root)
            self.assertEqual(len(findings), 4)
            self.assertTrue(any("unclosed" in item[2] and item[1] == 5 for item in findings))
            self.assertTrue(any("missing heading" in item[2] for item in findings))
            self.assertTrue(any("escapes repository" in item[2] for item in findings))

    def test_encoded_spaces_explicit_ids_reference_links_and_longer_fences(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "docs").mkdir()
            (root / "docs" / "a b.md").write_text('<a id="known"></a>\n# Header\n', encoding="utf-8")
            (root / "README.md").write_text("````md\n```\n[ignored](bad.md)\n````\n[x][ref]\n[ref]: docs/a%20b.md#known\n", encoding="utf-8")
            _, count, findings = docs.check(root)
            self.assertEqual((count, findings), (1, []))


if __name__ == "__main__":
    unittest.main()
