// ============================================================================
// parse.go 实现 qrels 与 run 两种评测文件格式的解析。
//
// qrels（trec qrels 风格，四列空格分隔，仅 rel>0 行）：
//
//	q-00 0 doc-00 3
//	q-00 0 doc-05 2
//
// 第二列为 iteration 字段，按惯例恒为 0（trec_eval 兼容，解析时要求为
// 整数但不限定取值）。rel<=0 的行被忽略（种子集口径只产出 rel>0 行）。
//
// run（三列或两列，空格分隔）：
//
//	q-00 doc-05 1      # qid docid rank
//	q-00 doc-00 2
//
// 三列格式按 rank 升序稳定排序（rank 相同保持文件行序）；两列格式
// （qid docid）按文件行序即视为 rank 升序。两列是 trec_eval 六列格式
// （qid Q0 docid rank score tag）的最小子集，转换方式见 README.md。
// ============================================================================

package evalseed

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// ParseQrels 从 qrels 文本解析分级标注。
//
// 规则：空行与 # 开头注释行跳过；每行必须四列；rel 解析为整数且 >=0
// （rel<=0 的行跳过不进结果）；同一 (qid, docid) 重复出现视为错误。
func ParseQrels(r io.Reader) (Qrels, error) {
	qrels := Qrels{}
	scanner := bufio.NewScanner(r)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 4 {
			return nil, fmt.Errorf("evalseed: qrels 第 %d 行字段数期望 4（qid iter docid rel），实际 %d", lineNo, len(fields))
		}
		if _, err := strconv.Atoi(fields[1]); err != nil {
			return nil, fmt.Errorf("evalseed: qrels 第 %d 行 iteration 字段 %q 不是整数", lineNo, fields[1])
		}
		rel, err := strconv.Atoi(fields[3])
		if err != nil {
			return nil, fmt.Errorf("evalseed: qrels 第 %d 行 rel 字段 %q 不是整数", lineNo, fields[3])
		}
		if rel < 0 {
			return nil, fmt.Errorf("evalseed: qrels 第 %d 行 rel=%d 不允许为负", lineNo, rel)
		}
		if rel == 0 {
			continue
		}
		qid, doc := fields[0], fields[2]
		if _, dup := qrels[qid][doc]; dup {
			return nil, fmt.Errorf("evalseed: qrels 第 %d 行 (%s,%s) 重复标注", lineNo, qid, doc)
		}
		if qrels[qid] == nil {
			qrels[qid] = map[string]int{}
		}
		qrels[qid][doc] = rel
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("evalseed: 读取 qrels 失败: %w", err)
	}
	return qrels, nil
}

// runEntry run 文件的中间条目：rank、出现顺序、docid。
type runEntry struct {
	rank int
	seq  int
	doc  string
}

// ParseRun 从 run 文本解析检索结果。
//
// 规则：空行与 # 开头注释行跳过；每行两列（qid docid，行序即排序）或
// 三列（qid docid rank，rank 须为 >=1 的整数，按 rank 升序稳定排序）；
// 同一 qid 内 docid 重复视为错误。
func ParseRun(r io.Reader) (Run, error) {
	type qidState struct {
		entries []runEntry
		seen    map[string]int
	}
	states := map[string]*qidState{}
	scanner := bufio.NewScanner(r)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 && len(fields) != 3 {
			return nil, fmt.Errorf("evalseed: run 第 %d 行字段数期望 2 或 3（qid docid [rank]），实际 %d", lineNo, len(fields))
		}
		qid, doc := fields[0], fields[1]
		rank := 0
		if len(fields) == 3 {
			var err error
			if rank, err = strconv.Atoi(fields[2]); err != nil {
				return nil, fmt.Errorf("evalseed: run 第 %d 行 rank 字段 %q 不是整数", lineNo, fields[2])
			}
			if rank < 1 {
				return nil, fmt.Errorf("evalseed: run 第 %d 行 rank=%d 必须 >=1", lineNo, rank)
			}
		}
		st, ok := states[qid]
		if !ok {
			st = &qidState{seen: map[string]int{}}
			states[qid] = st
		}
		if prev, dup := st.seen[doc]; dup {
			return nil, fmt.Errorf("evalseed: run 第 %d 行 doc %s 与第 %d 行在 qid %s 内重复", lineNo, doc, prev, qid)
		}
		st.seen[doc] = lineNo
		st.entries = append(st.entries, runEntry{rank: rank, seq: len(st.entries), doc: doc})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("evalseed: 读取 run 失败: %w", err)
	}

	run := make(Run, len(states))
	for qid, st := range states {
		entries := st.entries
		sort.SliceStable(entries, func(i, j int) bool {
			if entries[i].rank != entries[j].rank {
				return entries[i].rank < entries[j].rank
			}
			return entries[i].seq < entries[j].seq
		})
		docs := make([]string, len(entries))
		for i, e := range entries {
			docs[i] = e.doc
		}
		run[qid] = docs
	}
	return run, nil
}
