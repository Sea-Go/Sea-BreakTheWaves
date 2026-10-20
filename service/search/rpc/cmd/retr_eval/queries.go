// queries.go —— 评测侧的种子集查询装载与 gold 查询表示（dev 形态）。
//
// 语料装载与假编码已上提到 internal/devseed + internal/fakerepr（与
// cmd/search_demo、internal/pipeline 测试共享）；本文件只保留评测特有
// 的部分：queries.jsonl / qrels.txt 的解析，以及"查询表示来自 gold 文档"
// 的确定性查询表示构造（retr_eval 的 dev 口径，详见本目录 README.md）。
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/evalseed"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/fakerepr"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/retrieval"
)

// queryMultiTokens 是查询 multi 表示取主文档前 N 个 token 的 N（dev 口径）。
const queryMultiTokens = 8

// seedQuery 是 queries.jsonl 的一行（gold 以 qrels.txt 为准，此处只取
// qid 顺序与查询文本）。
type seedQuery struct {
	Qid  string
	Text string
}

// loadQueries 解析 queries.jsonl。
func loadQueries(seedsDir string) ([]seedQuery, error) {
	f, err := os.Open(filepath.Join(seedsDir, "queries.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("retr_eval: 读 queries.jsonl: %w", err)
	}
	defer f.Close()
	var queries []seedQuery
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		s := strings.TrimSpace(sc.Text())
		if s == "" {
			continue
		}
		var q struct {
			Qid  string `json:"qid"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(s), &q); err != nil {
			return nil, fmt.Errorf("retr_eval: queries.jsonl 第 %d 行: %w", line, err)
		}
		if q.Qid == "" {
			return nil, fmt.Errorf("retr_eval: queries.jsonl 第 %d 行缺 qid", line)
		}
		queries = append(queries, seedQuery{Qid: q.Qid, Text: q.Text})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("retr_eval: 读 queries.jsonl: %w", err)
	}
	if len(queries) == 0 {
		return nil, fmt.Errorf("retr_eval: queries.jsonl 无查询")
	}
	return queries, nil
}

// loadQrels 解析 qrels.txt。
func loadQrels(seedsDir string) (evalseed.Qrels, error) {
	f, err := os.Open(filepath.Join(seedsDir, "qrels.txt"))
	if err != nil {
		return nil, fmt.Errorf("retr_eval: 读 qrels.txt: %w", err)
	}
	defer f.Close()
	return evalseed.ParseQrels(f)
}

// buildRequest 按 gold 生成确定性查询表示（dev 口径：理想查询编码替身）。
//
//   - dense：全部相关文档（rel>0）去量 dense 的均值（按 doc_key 字典序
//     累加，浮点顺序固定）；
//   - sparse：相关文档种子文本的词频 hash（与假编码同口径，经
//     fakerepr.SparseMapOfText）；
//   - multi：主文档（rel 最高、平局字典序最小）multi 矩阵的前
//     queryMultiTokens 行。
func buildRequest(store *retrieval.Store, qrels evalseed.Qrels, qid string, tier retrieval.Tier) (retrieval.Request, error) {
	rels := qrels[qid]
	if len(rels) == 0 {
		return retrieval.Request{}, fmt.Errorf("qrels 缺少查询 %s 的 gold 标注", qid)
	}
	goldKeys := make([]string, 0, len(rels))
	for k := range rels {
		goldKeys = append(goldKeys, k)
	}
	sort.Strings(goldKeys)

	sn := store.Snapshot()
	var dense []float32
	primary := ""
	for _, k := range goldKeys {
		d, ok := sn.Doc(k)
		if !ok {
			return retrieval.Request{}, fmt.Errorf("gold 文档 %s 不在 Store 中", k)
		}
		if dense == nil {
			dense = append([]float32(nil), d.Dense...)
		} else {
			// Guard against gold docs with different dense vector lengths.
			n := len(dense)
			if len(d.Dense) < n {
				n = len(d.Dense)
			}
			for i := 0; i < n; i++ {
				dense[i] += d.Dense[i]
			}
		}
		if primary == "" || rels[k] > rels[primary] {
			primary = k
		}
	}
	for i := range dense {
		dense[i] /= float32(len(goldKeys))
	}

	// sparse：gold 种子文本的词频 hash（与文档侧编码同构；revision 段用
	// 确定性占位，真实查询编码属后续）。
	sparse := fakerepr.SparseMapOfText(goldSeedText(goldKeys))

	// multi：主文档 multi 矩阵前 N 行。
	pd, _ := sn.Doc(primary)
	multi := append([][]float32(nil), pd.Multi...)
	if len(multi) > queryMultiTokens {
		multi = multi[:queryMultiTokens]
	}
	return retrieval.Request{QueryID: qid, Tier: tier, Dense: dense, Sparse: sparse, Multi: multi}, nil
}

// goldSeedText 拼接 gold 文档的种子文本（与文档侧编码的种子同构：
// doc_key ‖ structure_ref ‖ revision_id；查询侧 revision 段用占位常量，
// 完整重现文档侧修订 ID 属真实编码器职责，dev 口径）。
func goldSeedText(goldKeys []string) string {
	s := ""
	for _, k := range goldKeys {
		s += k + "\x1fstructure/" + k + "\x1frev-seed\n"
	}
	return s
}
