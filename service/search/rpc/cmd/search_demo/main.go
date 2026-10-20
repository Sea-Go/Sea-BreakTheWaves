// Command search_demo 是端到端检索管线的 dev 演示入口：加载冻结种子集
// （internal/devseed）→ 建只读 Store → 装配默认管线（internal/pipeline）
// → 交互式查询 → 打印三档（fast/balanced/deep）× 两种交付
// （summary/tools）的结果（summary 含格式化引用列表）。
//
// 查询来源：命令行参数（一次性）或标准输入（逐行；TTY 下带提示符）。
// 每个查询块先打印判档路由（suggest × user → effective，MaxTier 只升
// 不降），再按交付形态打印答案+引用（summary）或候选证据包（tools）。
//
// 用法：
//
//	go run ./service/search/rpc/cmd/search_demo \
//	  [--seeds testdata/index/seeds] [--tier fast|balanced|deep|all] \
//	  [--delivery summary|tools|all] [查询…]
//
// 示例：
//
//	echo "海洋观测中观测网络建设的核心要点有哪些？" |
//	  go run ./service/search/rpc/cmd/search_demo --tier fast --delivery summary
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/devseed"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/pipeline"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/planner"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/retrieval"
	"github.com/Sea-Go/Sea-BreakTheWaves/service/search/rpc/internal/summary"
)

// topShown 是 tools 交付与 pack 概览展示的候选条数（展示截断，不影响
// 管线结果本身）。
const topShown = 5

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "search_demo: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	seeds := flag.String("seeds", "testdata/index/seeds", "种子集目录（含 corpus/）")
	tierFlag := flag.String("tier", "all", "检索档位：fast|balanced|deep|all")
	deliveryFlag := flag.String("delivery", "all", "交付形态：summary|tools|all")
	flag.Parse()

	tiers, err := parseTiers(*tierFlag)
	if err != nil {
		return err
	}
	deliveries, err := parseDeliveries(*deliveryFlag)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	queries := flag.Args()
	oneShot := len(queries) > 0
	if !oneShot {
		qs, err := readQueries(os.Stdin)
		if err != nil {
			return err
		}
		queries = qs
	}
	return runDemo(ctx, *seeds, tiers, deliveries, queries, oneShot, os.Stdout)
}

// runDemo 装配管线并对每个查询打印所选 档位×交付 的结果块。
// oneShot（命令行参数查询）下任一查询失败即返回错误；交互模式逐查询
// 报告后继续。
func runDemo(ctx context.Context, seedsDir string, tiers []retrieval.Tier, deliveries []pipeline.Delivery,
	queries []string, oneShot bool, w io.Writer) error {
	if seedsDir == "" {
		return fmt.Errorf("--seeds is required（种子集目录，见 --help）")
	}
	corpus, err := devseed.LoadCorpus(seedsDir)
	if err != nil {
		return err
	}
	p, err := pipeline.NewDefaultPipeline(corpus.Store)
	if err != nil {
		return fmt.Errorf("search_demo: 装配管线: %w", err)
	}
	fmt.Fprintf(w, "search_demo dev: seeds=%s docs=%d encoder=%s manifest_id=%s tiers=%v deliveries=%v\n",
		seedsDir, len(corpus.Docs), corpus.EncoderID, corpus.ManifestID, tiers, deliveries)

	router := planner.NewRouter()
	for _, query := range queries {
		fmt.Fprintf(w, "\n> %s\n", query)
		for _, tier := range tiers {
			suggest := router.Route(ctx, query)
			effective := planner.MaxTier(tier, suggest)
			for _, delivery := range deliveries {
				res, err := p.Execute(ctx, pipeline.PipelineRequest{Query: query, Tier: tier, Delivery: delivery})
				if err != nil {
					if oneShot {
						return fmt.Errorf("search_demo: 查询 %q（%s/%s）: %w", query, tier, delivery, err)
					}
					fmt.Fprintf(w, "--- tier=%s delivery=%s ---\nerror  : %v\n", tier, delivery, err)
					continue
				}
				writeBlock(w, tier, delivery, suggest, effective, res)
			}
		}
	}
	return nil
}

// writeBlock 打印一个 档位×交付 的结果块。
func writeBlock(w io.Writer, tier retrieval.Tier, delivery pipeline.Delivery,
	suggest, effective retrieval.Tier, res pipeline.PipelineResult) {
	fmt.Fprintf(w, "--- tier=%s delivery=%s ---\n", tier, delivery)
	fmt.Fprintf(w, "route  : suggest=%s × user=%s → effective=%s（MaxTier 只升不降）\n", suggest, tier, effective)
	fmt.Fprintf(w, "pack   : %d candidates\n", len(res.Pack.Candidates))
	writeTop(w, res)

	switch delivery {
	case pipeline.DeliverySummary:
		fmt.Fprintf(w, "answer : %s\n", indentLines(res.FormattedAnswer, "         "))
		fmt.Fprintf(w, "cites  : %s\n", indentLines(summary.RenderCitations(summary.SummaryResult{
			QueryID:   res.Pack.QueryID,
			Answer:    res.Answer,
			Citations: res.Citations,
		}), "         "))
	case pipeline.DeliveryTools:
		fmt.Fprintf(w, "tools  : EvidencePack 直返（不经 B6 摘要）\n")
	}
}

// writeTop 打印 pack 概览：前 topShown 个候选的 RRF 分数与路分数。
func writeTop(w io.Writer, res pipeline.PipelineResult) {
	n := topShown
	if len(res.Pack.Candidates) < n {
		n = len(res.Pack.Candidates)
	}
	if n == 0 {
		fmt.Fprintf(w, "top    : （无候选）\n")
		return
	}
	fmt.Fprintf(w, "top    :")
	for _, c := range res.Pack.Candidates[:n] {
		fmt.Fprintf(w, " %s(rrf=%.4f d=%.3f s=%.3f m=%.3f)", c.DocKey, c.RRFScore, c.Lanes.Dense, c.Lanes.Sparse, c.Lanes.Multi)
	}
	fmt.Fprintln(w)
}

// indentLines 给多行文本的每一行（首行除外）加缩进，对齐块内字段。
func indentLines(s, indent string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i := 1; i < len(lines); i++ {
		lines[i] = indent + lines[i]
	}
	return strings.Join(lines, "\n")
}

// parseTiers 解析 --tier（fast|balanced|deep|all；all 展开为三档序）。
func parseTiers(v string) ([]retrieval.Tier, error) {
	switch v {
	case "all":
		return []retrieval.Tier{retrieval.TierFast, retrieval.TierBalanced, retrieval.TierDeep}, nil
	case "fast":
		return []retrieval.Tier{retrieval.TierFast}, nil
	case "balanced":
		return []retrieval.Tier{retrieval.TierBalanced}, nil
	case "deep":
		return []retrieval.Tier{retrieval.TierDeep}, nil
	default:
		return nil, fmt.Errorf("--tier 非法值 %q（合法值 fast|balanced|deep|all）", v)
	}
}

// parseDeliveries 解析 --delivery（summary|tools|all）。
func parseDeliveries(v string) ([]pipeline.Delivery, error) {
	switch v {
	case "all":
		return []pipeline.Delivery{pipeline.DeliverySummary, pipeline.DeliveryTools}, nil
	case "summary":
		return []pipeline.Delivery{pipeline.DeliverySummary}, nil
	case "tools":
		return []pipeline.Delivery{pipeline.DeliveryTools}, nil
	default:
		return nil, fmt.Errorf("--delivery 非法值 %q（合法值 summary|tools|all）", v)
	}
}

// readQueries 逐行读标准输入：TTY 下先打印提示；空行跳过；每行
// TrimSpace 后作为一次查询。
func readQueries(r io.Reader) ([]string, error) {
	if f, ok := r.(*os.File); ok && isTerminal(f) {
		fmt.Fprintln(os.Stderr, "输入查询（每行一个，Ctrl-D 退出）：")
	}
	var queries []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if q := strings.TrimSpace(sc.Text()); q != "" {
			queries = append(queries, q)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("search_demo: 读标准输入: %w", err)
	}
	return queries, nil
}

// isTerminal 判断文件是否为字符设备（终端）——提示符只对交互输入打印，
// 管道输入保持输出干净。
func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}
