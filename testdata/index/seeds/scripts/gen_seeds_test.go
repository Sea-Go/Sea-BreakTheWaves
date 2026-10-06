// ============================================================================
// gen_seeds_test.go 校验种子集的两条根本性质：
//  1. 可再生成且逐字节一致（重新生成到临时目录与提交文件比对）；
//  2. 自洽性（段落计数、locator 引文逐字命中、qrels 与 queries.jsonl 对齐）。
//
// 运行方式（testdata 目录被 ./... 通配忽略，需显式指定路径）：
//
//	go test ./testdata/index/seeds/scripts/
// ============================================================================

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// seedsDir 提交在仓库中的种子集目录（脚本目录的上一级）。
func seedsDir(t *testing.T) string {
	t.Helper()
	return defaultSeedsDir()
}

// TestRegenerateByteIdentical 验证同一脚本重复生成逐字节一致。
func TestRegenerateByteIdentical(t *testing.T) {
	tmp := t.TempDir()
	if err := generateAll(tmp); err != nil {
		t.Fatalf("重新生成种子集失败: %v", err)
	}
	dir := seedsDir(t)

	artifacts := []string{"queries.jsonl", "qrels.txt"}
	for i := 0; i < numDocs; i++ {
		artifacts = append(artifacts, filepath.Join("corpus", docID(i)+".md"))
	}
	for _, rel := range artifacts {
		want, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil {
			t.Fatalf("读取提交文件 %s 失败: %v", rel, err)
		}
		got, err := os.ReadFile(filepath.Join(tmp, rel))
		if err != nil {
			t.Fatalf("读取再生文件 %s 失败: %v", rel, err)
		}
		if !bytes.Equal(want, got) {
			t.Errorf("%s 与再生成结果不一致（应逐字节相同）", rel)
		}
	}
}

// extractParas 从渲染后的 markdown 中抽取正文段落：
// 按空行分块，跳过标题（#）、导读（>）、分隔线（---）与注释（<!--）块。
// 与 README.md 中「段落计数口径」一致。
func extractParas(content string) []string {
	var paras []string
	for _, blk := range strings.Split(content, "\n\n") {
		blk = strings.TrimRight(blk, "\n")
		if blk == "" {
			continue
		}
		first := strings.SplitN(blk, "\n", 2)[0]
		switch {
		case strings.HasPrefix(first, "#"),
			strings.HasPrefix(first, ">"),
			strings.HasPrefix(first, "---"),
			strings.HasPrefix(first, "<!--"):
			continue
		}
		paras = append(paras, first)
	}
	return paras
}

// loadCorpus 读取全部提交文档，返回 docID → 段落切片。
func loadCorpus(t *testing.T) map[string][]string {
	t.Helper()
	corpus := make(map[string][]string, numDocs)
	for i := 0; i < numDocs; i++ {
		id := docID(i)
		raw, err := os.ReadFile(filepath.Join(seedsDir(t), "corpus", id+".md"))
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", id, err)
		}
		corpus[id] = extractParas(string(raw))
	}
	return corpus
}

// TestCorpusShape 验证 20 篇文档的命名、多级标题与 8-15 段正文约束，
// 且渲染结果与内存模型逐段一致。
func TestCorpusShape(t *testing.T) {
	docs := buildDocs()
	if len(docs) != numDocs {
		t.Fatalf("文档数期望 %d，实际 %d", numDocs, len(docs))
	}
	corpus := loadCorpus(t)
	for i, doc := range docs {
		if doc.ID != docID(i) {
			t.Errorf("第 %d 篇文档 ID 期望 %s，实际 %s", i, docID(i), doc.ID)
		}
		paras, ok := corpus[doc.ID]
		if !ok {
			t.Fatalf("缺少文档 %s", doc.ID)
		}
		if want := len(doc.Paragraphs); want < minParas || want > maxParas {
			t.Errorf("%s 段落数 %d 超出 [%d,%d]", doc.ID, want, minParas, maxParas)
		}
		if len(paras) != len(doc.Paragraphs) {
			t.Errorf("%s 渲染段落 %d 与模型段落 %d 不一致", doc.ID, len(paras), len(doc.Paragraphs))
			continue
		}
		for j, p := range paras {
			if p != doc.Paragraphs[j].Text {
				t.Errorf("%s 第 %d 段渲染文本与模型不一致", doc.ID, j)
			}
		}

		raw, err := os.ReadFile(filepath.Join(seedsDir(t), "corpus", doc.ID+".md"))
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", doc.ID, err)
		}
		content := string(raw)
		if got := countLinesWithPrefix(content, "# "); got != 1 {
			t.Errorf("%s 一级标题期望 1 个，实际 %d", doc.ID, got)
		}
		if got := countLinesWithPrefix(content, "## "); got != 3 {
			t.Errorf("%s 二级标题期望 3 个，实际 %d", doc.ID, got)
		}
		if got := countLinesWithPrefix(content, "### "); got < 1 {
			t.Errorf("%s 三级标题期望至少 1 个，实际 %d", doc.ID, got)
		}
	}
}

// countLinesWithPrefix 统计以指定前缀开头的行数（"# "/"## "/"### "互不重叠）。
func countLinesWithPrefix(content, prefix string) int {
	n := 0
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, prefix) {
			n++
		}
	}
	return n
}

// TestQueriesConsistency 验证 50 条查询的编号连续、gold 指向存在的文档、
// 分级在 1..3、locator 引文逐字命中且指向 rel>=2 的文档。
func TestQueriesConsistency(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(seedsDir(t), "queries.jsonl"))
	if err != nil {
		t.Fatalf("读取 queries.jsonl 失败: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != numQueries {
		t.Fatalf("查询数期望 %d，实际 %d", numQueries, len(lines))
	}
	corpus := loadCorpus(t)

	for i, line := range lines {
		var q queryJSON
		if err := json.Unmarshal([]byte(line), &q); err != nil {
			t.Fatalf("第 %d 行 JSON 解析失败: %v", i, err)
		}
		if want := fmt.Sprintf("q-%02d", i); q.Qid != want {
			t.Errorf("第 %d 行 qid 期望 %s，实际 %s", i, want, q.Qid)
		}
		if len(q.Gold.DocRels) < 2 {
			t.Errorf("%s gold 文档数 %d 少于 2", q.Qid, len(q.Gold.DocRels))
		}
		for doc, rel := range q.Gold.DocRels {
			paras, ok := corpus[doc]
			if !ok {
				t.Errorf("%s gold 指向不存在的文档 %s", q.Qid, doc)
				continue
			}
			if rel < 1 || rel > 3 {
				t.Errorf("%s 对 %s 的分级 %d 超出 [1,3]", q.Qid, doc, rel)
			}
			_ = paras
		}
		if len(q.Gold.Locators) == 0 {
			t.Errorf("%s 缺少 locator_spans", q.Qid)
		}
		for _, sp := range q.Gold.Locators {
			paras, ok := corpus[sp.Doc]
			if !ok {
				t.Errorf("%s locator 指向不存在的文档 %s", q.Qid, sp.Doc)
				continue
			}
			if sp.ParaIndex < 0 || sp.ParaIndex >= len(paras) {
				t.Errorf("%s locator 段落索引 %d 超出 %s 的范围 [0,%d)", q.Qid, sp.ParaIndex, sp.Doc, len(paras))
				continue
			}
			if !strings.Contains(paras[sp.ParaIndex], sp.Quote) {
				t.Errorf("%s locator 引文未逐字命中 %s 第 %d 段", q.Qid, sp.Doc, sp.ParaIndex)
			}
			if q.Gold.DocRels[sp.Doc] < 2 {
				t.Errorf("%s locator 指向的 %s 相关性 %d 低于 2", q.Qid, sp.Doc, q.Gold.DocRels[sp.Doc])
			}
		}
	}
}

// TestQrelsMatchesQueries 验证 qrels.txt 只含 rel>0 行、格式正确、
// 与 queries.jsonl 的 doc_rels 完全对齐且有序。
func TestQrelsMatchesQueries(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(seedsDir(t), "qrels.txt"))
	if err != nil {
		t.Fatalf("读取 qrels.txt 失败: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")

	qraw, err := os.ReadFile(filepath.Join(seedsDir(t), "queries.jsonl"))
	if err != nil {
		t.Fatalf("读取 queries.jsonl 失败: %v", err)
	}
	type relKey struct{ qid, doc string }
	want := map[relKey]int{}
	for _, line := range strings.Split(strings.TrimRight(string(qraw), "\n"), "\n") {
		var q queryJSON
		if err := json.Unmarshal([]byte(line), &q); err != nil {
			t.Fatalf("解析 queries.jsonl 失败: %v", err)
		}
		for doc, rel := range q.Gold.DocRels {
			if rel > 0 {
				want[relKey{q.Qid, doc}] = rel
			}
		}
	}
	if len(lines) != len(want) {
		t.Errorf("qrels 行数 %d 与期望 %d 不一致", len(lines), len(want))
	}

	prev := ""
	got := map[relKey]int{}
	for i, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 4 {
			t.Errorf("第 %d 行字段数期望 4，实际 %d", i+1, len(fields))
			continue
		}
		if fields[1] != "0" {
			t.Errorf("第 %d 行 iteration 字段期望 0，实际 %s", i+1, fields[1])
		}
		key := relKey{fields[0], fields[2]}
		if cur := key.qid + "\x00" + key.doc; prev != "" && cur <= prev {
			t.Errorf("第 %d 行未按 (qid,docid) 严格升序", i+1)
		} else {
			prev = cur
		}
		var rel int
		if rel, err = strconv.Atoi(fields[3]); err != nil {
			t.Errorf("第 %d 行 rel 解析失败: %v", i+1, err)
			continue
		}
		got[key] = rel
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("qrels(%s,%s) 期望 %d，实际 %d", k.qid, k.doc, v, got[k])
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Errorf("qrels 含 queries.jsonl 之外的条目 (%s,%s)", k.qid, k.doc)
		}
	}
}
