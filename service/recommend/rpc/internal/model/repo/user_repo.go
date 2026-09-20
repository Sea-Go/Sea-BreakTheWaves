package repo

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"sea/service/recommend/rpc/internal/domain"
)

// ============================================================================
// 该文件实现 UserRepository interface 与 userRepoAdapter 聚合适配器。
//
// userRepoAdapter 聚合多个 UserStore 子仓库（5个：静态/动态/行为/时间/会话），
// Load 时并行从所有 stores 加载并合并为完整 UserProfile，子错误聚合为
// PartialErrors 返回（不中断主流程）；UpdateDynamic/UpdateTemporal 写入对应 store。
//
// 二开扩展点：
//   - 替换子仓库：实现 UserStore interface 并通过 NewUserRepoAdapter 注入
//   - 新增子仓库：实现 UserStore 并追加到 stores（如接入新的数据源）
//   - 时间画像支持：UserStore 实现者可额外实现 temporalStore 扩展接口
//   - PartialErrors：多源聚合时部分失败通过 PartialErrors 返回，调用方可按需处理
// ============================================================================

// 编译期断言：*userRepoAdapter 实现 UserRepository interface。
var _ UserRepository = (*userRepoAdapter)(nil)

// UserRepository 用户仓储 interface。
//
// 聚合多数据源（静态/动态/行为/时间/会话）提供统一用户画像读写能力。
// Load 返回完整 UserProfile（部分失败时返回已合并部分 + PartialErrors）。
//
// 二开扩展点：实现该 interface 替换默认 userRepoAdapter；
// 或通过 NewUserRepoAdapter 注入自定义 UserStore 列表。
type UserRepository interface {
	// Load 加载用户画像（聚合多 store）。
	// ctx 上下文；key 用户标识。
	// 返回合并后的 UserProfile 与 error（部分失败时 error 为 *PartialErrors）。
	Load(ctx context.Context, key domain.UserKey) (*domain.UserProfile, error)
	// UpdateDynamic 更新动态画像（写入对应 store）。
	UpdateDynamic(ctx context.Context, key domain.UserKey, dp domain.DynamicProfile) error
	// UpdateTemporal 更新时间画像（写入实现 temporalStore 的 store）。
	UpdateTemporal(ctx context.Context, key domain.UserKey, tp domain.TemporalProfile) error
	// LoadTemporal 加载时间画像（从实现 temporalStore 的 store 读取）。
	LoadTemporal(ctx context.Context, key domain.UserKey) (*domain.TemporalProfile, error)
}

// UserStore 子仓库抽象 interface。
//
// 每个子仓库负责画像的某一部分（如静态/动态/行为/时间/会话），
// Load 返回部分填充的 UserProfile（仅该仓库负责的字段非 nil），
// Save 写入完整 UserProfile（子仓库按需提取自己负责的字段）。
//
// 二开扩展点：业务方实现该 interface 接入新数据源（如 Redis/Milvus/Postgres），
// 通过 NewUserRepoAdapter 注入即可扩展聚合能力，无需修改 userRepoAdapter。
// 实现者可额外实现 temporalStore 扩展接口以支持 TemporalProfile 读写。
type UserStore interface {
	// Name 返回子仓库名称（用于错误定位）。
	Name() string
	// Load 加载该子仓库负责的部分画像。
	// 返回部分填充的 UserProfile（其他字段为 nil）与 error。
	Load(ctx context.Context, key domain.UserKey) (domain.UserProfile, error)
	// Save 保存画像到该子仓库（子仓库按需提取自己负责的字段）。
	Save(ctx context.Context, key domain.UserKey, profile domain.UserProfile) error
}

// temporalStore 时间画像子仓库扩展接口（可选）。
//
// UserStore 实现者可额外实现该接口以支持 TemporalProfile 读写。
// userRepoAdapter 的 UpdateTemporal/LoadTemporal 会通过类型断言检测并路由
// 到实现该接口的 store。
//
// 二开扩展点：实现该接口的 UserStore 可被 userRepoAdapter 用于
// UpdateTemporal/LoadTemporal，无需修改 userRepoAdapter 代码。
type temporalStore interface {
	// LoadTemporal 加载时间画像。
	LoadTemporal(ctx context.Context, key domain.UserKey) (*domain.TemporalProfile, error)
	// SaveTemporal 保存时间画像。
	SaveTemporal(ctx context.Context, key domain.UserKey, tp domain.TemporalProfile) error
}

// PartialErrors 部分错误聚合，多源聚合时部分子仓库失败时返回。
//
// 实现 error interface，Error() 返回所有子错误的聚合信息。
// 调用方可通过 errors.As 提取 Errors 列表按需处理。
//
// 二开：调用方可根据 PartialErrors 决定是否降级（如部分画像缺失时走兜底策略）。
type PartialErrors struct {
	// Errors 子错误列表。
	Errors []error
}

// Error 实现 error interface，返回聚合错误信息。
func (p *PartialErrors) Error() string {
	if p == nil || len(p.Errors) == 0 {
		return "no errors"
	}
	msgs := make([]string, 0, len(p.Errors))
	for _, e := range p.Errors {
		msgs = append(msgs, e.Error())
	}
	return fmt.Sprintf("partial errors (%d): %s", len(p.Errors), strings.Join(msgs, "; "))
}

// userRepoAdapter UserRepository 的默认实现。
//
// 聚合多个 UserStore，Load 时并行加载合并，子错误聚合为 PartialErrors。
// UpdateDynamic 通过 Save 写入所有 store（各 store 按需提取字段）。
// UpdateTemporal/LoadTemporal 路由到实现 temporalStore 扩展接口的 store。
//
// 二开扩展点：通过 NewUserRepoAdapter 替换/新增 UserStore。
type userRepoAdapter struct {
	stores []UserStore
}

// NewUserRepoAdapter 创建用户仓储适配器。
//
// stores 为子仓库列表（如 5 个：静态/动态/行为/时间/会话）。
// 二开：业务方可传入任意数量的 UserStore 实现扩展聚合能力。
func NewUserRepoAdapter(stores ...UserStore) *userRepoAdapter {
	return &userRepoAdapter{stores: stores}
}

// Load 并行从所有 stores 加载用户画像并合并。
//
// 流程：
//  1. 并行调用每个 store.Load
//  2. 合并所有成功返回的部分 UserProfile（非 nil 字段覆盖）
//  3. 失败的 store 错误聚合为 PartialErrors
//  4. 全部失败时返回 nil + error；部分失败时返回已合并画像 + PartialErrors；
//     全部成功时返回完整画像 + nil
//
// ctx 上下文；key 用户标识。
// 返回合并后的 UserProfile 与 error（部分失败时 error 为 *PartialErrors）。
func (a *userRepoAdapter) Load(ctx context.Context, key domain.UserKey) (*domain.UserProfile, error) {
	n := len(a.stores)
	if n == 0 {
		return nil, errors.New("userRepoAdapter: no stores configured")
	}

	type loadResult struct {
		profile domain.UserProfile
		err     error
	}
	results := make([]loadResult, n)

	// 并行从所有 stores 加载
	var wg sync.WaitGroup
	wg.Add(n)
	for i, s := range a.stores {
		go func(idx int, store UserStore) {
			defer wg.Done()
			p, err := store.Load(ctx, key)
			results[idx] = loadResult{profile: p, err: err}
		}(i, s)
	}
	wg.Wait()

	// 合并画像
	merged := &domain.UserProfile{}
	var partialErrs []error
	successCount := 0
	for i, r := range results {
		if r.err != nil {
			partialErrs = append(partialErrs, fmt.Errorf("store %q: %w", a.stores[i].Name(), r.err))
			continue
		}
		successCount++
		// 合并非 nil 字段（后成功覆盖先成功，业务上各 store 负责不同字段）
		if r.profile.Static != nil {
			merged.Static = r.profile.Static
		}
		if r.profile.Dynamic != nil {
			merged.Dynamic = r.profile.Dynamic
		}
		if r.profile.Behavior != nil {
			merged.Behavior = r.profile.Behavior
		}
	}

	if successCount == 0 {
		// 全部失败：返回聚合错误
		return nil, &PartialErrors{Errors: partialErrs}
	}
	if len(partialErrs) > 0 {
		// 部分失败：返回已合并画像 + PartialErrors
		return merged, &PartialErrors{Errors: partialErrs}
	}
	return merged, nil
}

// UpdateDynamic 更新动态画像，通过 Save 写入所有 store。
//
// 构造仅含 Dynamic 字段的 UserProfile，调用各 store.Save；
// 各 store 按需提取自己负责的字段（Dynamic store 写入，其他 store 跳过）。
//
// 二开：子仓库按需从 profile 提取自己负责的字段。
func (a *userRepoAdapter) UpdateDynamic(ctx context.Context, key domain.UserKey, dp domain.DynamicProfile) error {
	profile := domain.UserProfile{Dynamic: &dp}
	return a.saveAll(ctx, key, profile)
}

// UpdateTemporal 更新时间画像，写入实现 temporalStore 扩展接口的 store。
//
// 遍历所有 store，通过类型断言检测是否实现 temporalStore，
// 对实现的 store 调用 SaveTemporal。若无 store 实现该接口，返回 error。
// 部分失败时错误聚合为 PartialErrors。
//
// 二开扩展点：UserStore 实现者实现 temporalStore 接口即可支持时间画像读写。
func (a *userRepoAdapter) UpdateTemporal(ctx context.Context, key domain.UserKey, tp domain.TemporalProfile) error {
	var errs []error
	found := false
	for _, s := range a.stores {
		ts, ok := s.(temporalStore)
		if !ok {
			continue
		}
		found = true
		if err := ts.SaveTemporal(ctx, key, tp); err != nil {
			errs = append(errs, fmt.Errorf("store %q: %w", s.Name(), err))
		}
	}
	if !found {
		return errors.New("userRepoAdapter: no store implements temporalStore")
	}
	if len(errs) == 0 {
		return nil
	}
	return &PartialErrors{Errors: errs}
}

// LoadTemporal 加载时间画像，从实现 temporalStore 扩展接口的 store 读取。
//
// 遍历所有 store，返回首个成功加载的 TemporalProfile。
// 若无 store 实现该接口，返回 error。
//
// 二开扩展点：UserStore 实现者实现 temporalStore 接口即可支持时间画像读写。
func (a *userRepoAdapter) LoadTemporal(ctx context.Context, key domain.UserKey) (*domain.TemporalProfile, error) {
	var errs []error
	found := false
	for _, s := range a.stores {
		ts, ok := s.(temporalStore)
		if !ok {
			continue
		}
		found = true
		tp, err := ts.LoadTemporal(ctx, key)
		if err != nil {
			errs = append(errs, fmt.Errorf("store %q: %w", s.Name(), err))
			continue
		}
		return tp, nil
	}
	if !found {
		return nil, errors.New("userRepoAdapter: no store implements temporalStore")
	}
	if len(errs) > 0 {
		return nil, &PartialErrors{Errors: errs}
	}
	return nil, errors.New("userRepoAdapter: temporal store returned no data")
}

// saveAll 并行写入所有 store，聚合错误为 PartialErrors。
//
// 返回 nil（全部成功）或 *PartialErrors（部分/全部失败）。
func (a *userRepoAdapter) saveAll(ctx context.Context, key domain.UserKey, profile domain.UserProfile) error {
	n := len(a.stores)
	if n == 0 {
		return errors.New("userRepoAdapter: no stores configured")
	}
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i, s := range a.stores {
		go func(idx int, store UserStore) {
			defer wg.Done()
			errs[idx] = store.Save(ctx, key, profile)
		}(i, s)
	}
	wg.Wait()

	var partialErrs []error
	for i, e := range errs {
		if e != nil {
			partialErrs = append(partialErrs, fmt.Errorf("store %q: %w", a.stores[i].Name(), e))
		}
	}
	if len(partialErrs) == 0 {
		return nil
	}
	return &PartialErrors{Errors: partialErrs}
}
