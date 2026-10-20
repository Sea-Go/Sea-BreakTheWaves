// objectkey.go —— 索引工件的对象键约定（唯一实现）。
//
// 键布局：<manifest_id>/<内容寻址 ref>，manifest 与检索树为固定名对象。
// 给定 manifest_id 与 ref，键是纯函数，重试必然重写同一键（不半提交与
// 幂等重放依赖此性质）。
//
// 此前该约定在 async/internal/indexer 与 search/internal/retrieval 的镜像
// 文件里各有一份实现，2026-10-08 复用审计后上提到本包统一。
package wholeindex

const (
	// ManifestObject 是清单对象在 manifest_id 前缀下的固定名。
	ManifestObject = "manifest.v1.json"
	// TreeObject 是检索树对象在 manifest_id 前缀下的固定名。
	TreeObject = "tree.v1.json"
)

// ObjectKey 返回单文档载荷的对象键：manifest_id 前缀 + 内容寻址 ref。
func ObjectKey(manifestID, ref string) string {
	return manifestID + "/" + ref
}

// ManifestKey 返回清单对象键。
func ManifestKey(manifestID string) string {
	return manifestID + "/" + ManifestObject
}

// TreeKey 返回检索树对象键。
func TreeKey(manifestID string) string {
	return manifestID + "/" + TreeObject
}
