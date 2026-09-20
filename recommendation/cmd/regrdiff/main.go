// Package main 实现了推荐服务的回归基线对比工具 regrdiff。
//
// 用途：
//   在推荐系统重构（trpc-agent refactor）过程中，需要一套“黄金文件（golden files）”
//   作为回归基线。本工具对比“基线目录（-baseline）”与“新响应目录（-new）”中按
//   文件名配对的 JSON 快照，输出逐字段的差异报告，从而在重构后快速发现行为回退。
//
// 用法：
//   go run ./cmd/regrdiff -baseline testdata/baseline -new testdata/candidate \
//       [-ignore trace_id,timestamp,latency_ms] [-verbose]
//
// 依赖：仅使用 Go 标准库，便于在无外部代理（GOPROXY 不可用）时本地编译。
//
// 退出码：
//   - 0：所有配对文件在忽略指定字段后完全一致（回归通过）
//   - 1：存在差异、缺少配对文件或发生错误（回归失败）
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// defaultIgnoreFields 为默认忽略的易变字段集合（字段名全部小写匹配）。
// 这些字段每次请求都会变化（链路 ID、时间戳等），即使不传 -ignore 也会被跳过。
// 用户可通过 -ignore 追加额外需要忽略的字段。
var defaultIgnoreFields = map[string]struct{}{
	"trace_id":          {},
	"rec_request_id":    {},
	"request_id":        {},
	"search_request_id": {},
	"timestamp":         {},
	"created_at":        {},
	"updated_at":        {},
}

// Diff 表示单条字段级差异。
type Diff struct {
	Path     string `json:"path"`     // 字段路径，如 data.ids[2] 或 data.explain_trace[0].name
	Baseline any    `json:"baseline"` // 基线侧的值（缺失时为 nil）
	New      any    `json:"new"`      // 新侧的值（缺失时为 nil）
	Reason   string `json:"reason"`   // 差异原因：type_mismatch / value_mismatch / missing_in_baseline / missing_in_new / length_mismatch
}

// FileResult 表示单个文件对的对比结果。
type FileResult struct {
	File    string `json:"file"`    // 文件名（相对名）
	Pass    bool   `json:"pass"`    // 是否一致（忽略指定字段后）
	Diffs   []Diff `json:"diffs"`   // 字段级差异列表
	Skipped bool   `json:"skipped"` // 是否因缺少配对而被跳过
	Reason  string `json:"reason"`  // 跳过/错误原因
}

// Report 为整体对比报告。
type Report struct {
	BaselineDir string       `json:"baseline_dir"`
	NewDir      string       `json:"new_dir"`
	Total       int          `json:"total"`    // 配对文件总数
	Matched     int          `json:"matched"`  // 一致数
	Mismatched  int          `json:"mismatched"` // 不一致数
	Missing     int          `json:"missing"`  // 缺少配对数
	Results     []FileResult `json:"results"`
	AllPass     bool         `json:"all_pass"` // 整体是否通过
}

func main() {
	baselineDir := flag.String("baseline", "testdata/baseline", "基线目录（黄金文件所在目录）")
	newDir := flag.String("new", "", "新响应目录（必填，待对比的响应所在目录）")
	ignoreStr := flag.String("ignore", "", "额外忽略的字段名，逗号分隔（如 trace_id,timestamp,latency_ms）")
	verbose := flag.Bool("verbose", false, "打印完整逐字段差异")
	flag.Parse()

	if *newDir == "" {
		fmt.Fprintln(os.Stderr, "错误：-new 为必填参数")
		flag.Usage()
		os.Exit(1)
	}

	// 合并默认忽略字段与用户通过 -ignore 追加的字段
	ignoreSet := make(map[string]struct{}, len(defaultIgnoreFields)+4)
	for k := range defaultIgnoreFields {
		ignoreSet[k] = struct{}{}
	}
	if *ignoreStr != "" {
		for _, f := range strings.Split(*ignoreStr, ",") {
			f = strings.TrimSpace(strings.ToLower(f))
			if f != "" {
				ignoreSet[f] = struct{}{}
			}
		}
	}

	report, err := run(*baselineDir, *newDir, ignoreSet)
	if err != nil {
		fmt.Fprintf(os.Stderr, "对比失败: %v\n", err)
		os.Exit(1)
	}

	printReport(report, *verbose)

	if !report.AllPass {
		os.Exit(1)
	}
	os.Exit(0)
}

// run 执行整体对比流程：收集两侧文件 -> 配对 -> 逐对比对 -> 汇总。
func run(baselineDir, newDir string, ignoreSet map[string]struct{}) (*Report, error) {
	report := &Report{
		BaselineDir: baselineDir,
		NewDir:      newDir,
		AllPass:     true,
	}

	baseFiles, err := listCaseJSONFiles(baselineDir)
	if err != nil {
		return nil, fmt.Errorf("读取基线目录失败: %w", err)
	}
	newFiles, err := listCaseJSONFiles(newDir)
	if err != nil {
		return nil, fmt.Errorf("读取新响应目录失败: %w", err)
	}

	// 以基线文件为主，按文件名配对；同时记录新侧独有的文件。
	newSet := make(map[string]string, len(newFiles))
	for name, path := range newFiles {
		newSet[name] = path
	}

	// 为输出稳定，先对基线文件名排序
	baseNames := make([]string, 0, len(baseFiles))
	for name := range baseFiles {
		baseNames = append(baseNames, name)
	}
	sort.Strings(baseNames)

	for _, name := range baseNames {
		report.Total++
		fr := FileResult{File: name}
		basePath := baseFiles[name]
		newPath, ok := newSet[name]
		if !ok {
			// 新侧缺少对应文件
			fr.Skipped = true
			fr.Reason = "新响应目录中缺少该文件"
			report.Missing++
			report.AllPass = false
			report.Results = append(report.Results, fr)
			continue
		}
		delete(newSet, name)

		diffs, err := compareFiles(basePath, newPath, ignoreSet)
		if err != nil {
			fr.Skipped = true
			fr.Reason = "解析/对比出错: " + err.Error()
			report.Missing++
			report.AllPass = false
			report.Results = append(report.Results, fr)
			continue
		}
		fr.Diffs = diffs
		fr.Pass = len(diffs) == 0
		if fr.Pass {
			report.Matched++
		} else {
			report.Mismatched++
			report.AllPass = false
		}
		report.Results = append(report.Results, fr)
	}

	// 新侧独有文件（基线侧缺失），按名排序后作为跳过项
	newOnly := make([]string, 0, len(newSet))
	for name := range newSet {
		newOnly = append(newOnly, name)
	}
	sort.Strings(newOnly)
	for _, name := range newOnly {
		report.Total++
		report.Results = append(report.Results, FileResult{
			File:    name,
			Skipped: true,
			Reason:  "基线目录中缺少该文件",
		})
		report.Missing++
		report.AllPass = false
	}

	return report, nil
}

// listCaseJSONFiles 收集目录下所有 case_*.json 文件，返回“文件名 -> 绝对路径”映射。
// 文件名取 basename，配对基于同名文件。
func listCaseJSONFiles(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// 仅匹配 case_*.json，避免误读 README 等非数据文件
		if !strings.HasPrefix(name, "case_") {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		full, err := filepath.Abs(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		out[name] = full
	}
	return out, nil
}

// compareFiles 读取并对比两个 JSON 文件，返回字段级差异列表。
// ignoreSet 中的字段名（小写）在对比时会被跳过。
func compareFiles(basePath, newPath string, ignoreSet map[string]struct{}) ([]Diff, error) {
	baseBytes, err := os.ReadFile(basePath)
	if err != nil {
		return nil, fmt.Errorf("读取基线文件: %w", err)
	}
	newBytes, err := os.ReadFile(newPath)
	if err != nil {
		return nil, fmt.Errorf("读取新响应文件: %w", err)
	}

	var base, newVal any
	if err := json.Unmarshal(baseBytes, &base); err != nil {
		return nil, fmt.Errorf("解析基线 JSON: %w", err)
	}
	if err := json.Unmarshal(newBytes, &newVal); err != nil {
		return nil, fmt.Errorf("解析新响应 JSON: %w", err)
	}

	var diffs []Diff
	deepDiff("$", base, newVal, ignoreSet, &diffs)
	return diffs, nil
}

// deepDiff 递归对比两个已解析的 JSON 值，将差异追加到 diffs。
// path 为当前字段的路径表达（根节点为 "$"）。
func deepDiff(path string, base, newVal any, ignoreSet map[string]struct{}, diffs *[]Diff) {
	// 类型不一致直接记一条差异（nil 与非 nil 需单独处理，避免误报）
	if reflect.TypeOf(base) != reflect.TypeOf(newVal) {
		if base == nil || newVal == nil {
			*diffs = append(*diffs, Diff{
				Path: path, Baseline: base, New: newVal,
				Reason: valueMismatchReason(base, newVal),
			})
			return
		}
		*diffs = append(*diffs, Diff{
			Path: path, Baseline: base, New: newVal, Reason: "type_mismatch",
		})
		return
	}

	switch bv := base.(type) {
	case map[string]any:
		cv := newVal.(map[string]any)
		diffMaps(path, bv, cv, ignoreSet, diffs)
	case []any:
		cv := newVal.([]any)
		diffSlices(path, bv, cv, ignoreSet, diffs)
	default:
		// 标量：直接比较
		if !reflect.DeepEqual(base, newVal) {
			*diffs = append(*diffs, Diff{
				Path: path, Baseline: base, New: newVal, Reason: "value_mismatch",
			})
		}
	}
}

// valueMismatchReason 处理 nil 与非 nil 的差异原因描述。
func valueMismatchReason(base, newVal any) string {
	if base == nil && newVal != nil {
		return "missing_in_baseline"
	}
	if base != nil && newVal == nil {
		return "missing_in_new"
	}
	return "value_mismatch"
}

// diffMaps 对比两个 JSON 对象，逐字段递归。在 ignoreSet 中的字段直接跳过。
func diffMaps(path string, base, newVal map[string]any, ignoreSet map[string]struct{}, diffs *[]Diff) {
	// 收集两侧所有 key，排序保证输出稳定
	keys := make(map[string]struct{}, len(base)+len(newVal))
	for k := range base {
		keys[k] = struct{}{}
	}
	for k := range newVal {
		keys[k] = struct{}{}
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)

	for _, k := range sorted {
		// 命中忽略集合的字段直接跳过（无论是否缺失都不计入差异）
		if _, ignored := ignoreSet[strings.ToLower(k)]; ignored {
			continue
		}
		childPath := fmt.Sprintf("%s.%s", path, k)
		bv, bok := base[k]
		cv, cok := newVal[k]
		switch {
		case bok && cok:
			deepDiff(childPath, bv, cv, ignoreSet, diffs)
		case bok && !cok:
			*diffs = append(*diffs, Diff{
				Path: childPath, Baseline: bv, New: nil, Reason: "missing_in_new",
			})
		case !bok && cok:
			*diffs = append(*diffs, Diff{
				Path: childPath, Baseline: nil, New: cv, Reason: "missing_in_baseline",
			})
		}
	}
}

// diffSlices 对比两个 JSON 数组：长度不同记一条；公共下标逐元素递归。
func diffSlices(path string, base, newVal []any, ignoreSet map[string]struct{}, diffs *[]Diff) {
	if len(base) != len(newVal) {
		*diffs = append(*diffs, Diff{
			Path: path, Baseline: len(base), New: len(newVal), Reason: "length_mismatch",
		})
	}
	min := len(base)
	if len(newVal) < min {
		min = len(newVal)
	}
	for i := 0; i < min; i++ {
		childPath := fmt.Sprintf("%s[%d]", path, i)
		deepDiff(childPath, base[i], newVal[i], ignoreSet, diffs)
	}
}

// printReport 输出对比报告。verbose=true 时打印每个失败用例的完整逐字段差异。
func printReport(r *Report, verbose bool) {
	fmt.Printf("回归对比报告\n")
	fmt.Printf("  基线目录: %s\n", r.BaselineDir)
	fmt.Printf("  新响应目录: %s\n", r.NewDir)
	fmt.Printf("  配对文件: %d  一致: %d  不一致: %d  缺失: %d\n\n", r.Total, r.Matched, r.Mismatched, r.Missing)

	for _, fr := range r.Results {
		status := "PASS"
		if fr.Skipped {
			status = "SKIP"
		} else if !fr.Pass {
			status = "FAIL"
		}
		fmt.Printf("[%s] %s\n", status, fr.File)
		if fr.Skipped {
			fmt.Printf("    原因: %s\n", fr.Reason)
			continue
		}
		if !fr.Pass {
			// 非 verbose 模式仅打印差异条数；verbose 模式打印完整字段差异
			if !verbose {
				fmt.Printf("    差异条数: %d（使用 -verbose 查看详情）\n", len(fr.Diffs))
				continue
			}
			for _, d := range fr.Diffs {
				fmt.Printf("    - %s\n", d.Path)
				fmt.Printf("        原因: %s\n", d.Reason)
				fmt.Printf("        基线: %s\n", formatValue(d.Baseline))
				fmt.Printf("        新版: %s\n", formatValue(d.New))
			}
		}
	}

	fmt.Println()
	if r.AllPass {
		fmt.Println("结论: 全部通过（忽略指定字段后一致）")
	} else {
		fmt.Println("结论: 存在差异，回归未通过")
	}
}

// formatValue 将差异值格式化为可读字符串。
func formatValue(v any) string {
	if v == nil {
		return "<nil>"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}
