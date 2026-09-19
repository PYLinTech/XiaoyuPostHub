package server

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/blobstore"
	"github.com/PYLinTech/XiaoyuPostHub/backend/resource"
)

// resourceBlob 读取资源对应的物理对象元数据。
func resourceBlob(ctx context.Context, deps Deps, item resource.Resource) (blobstore.Blob, error) {
	if item.BlobID == nil {
		return blobstore.Blob{}, blobstore.ErrBlobNotFound
	}
	return deps.Blobs.Get(ctx, *item.BlobID)
}

// contentSHA256Header 携带交付内容的明文 SHA-256，供接收方（浏览器端）在
// 下载完成后自行校验。
const contentSHA256Header = "X-XPH-Content-SHA256"

// setContentSHA256Header 在响应头声明本次交付内容的明文哈希。
func setContentSHA256Header(w http.ResponseWriter, sum string) {
	if strings.TrimSpace(sum) != "" {
		w.Header().Set(contentSHA256Header, sum)
	}
}

// blobReader 打开对象的明文读取流。
//
// 交付路径不做服务端全量校验：完整读取一遍再回放等于把文件读两遍，对远端
// 后端还会使出站流量翻倍。完整性由两处保证——
//  1. 上传时：服务端在写入过程中流式计算 SHA-256 并与前端声明的哈希比对
//     （零额外 IO，写入不一致直接失败，不会落库）；
//  2. 接收时：浏览器下载/解密完成后按响应头 X-XPH-Content-SHA256 自行校验。
func blobReader(ctx context.Context, deps Deps, item resource.Resource) (io.ReadSeekCloser, blobstore.Blob, error) {
	blob, err := resourceBlob(ctx, deps, item)
	if err != nil {
		return nil, blobstore.Blob{}, err
	}
	reader, err := deps.Blobs.Open(ctx, blob)
	if err != nil {
		return nil, blobstore.Blob{}, err
	}
	return reader, blob, nil
}

// deliverBlobContent 输出一次对象内容，返回实际发送区间（供调用方结算统计）。
// 三条交付路径（统一会话 / 资源下载与预览 / 分享下载）共用，避免"密文分支 +
// 头设置 + 流式输出"三段拷贝各自漂移。
//
//   - clientDecrypt 为真：输出密文，并在响应头下发解密元数据（meta 为 nil 时
//     只下发内容哈希），由浏览器端解密；
//   - 否则：由存储层解密后输出明文（明文对象即原样输出）。
//
// ok=false 表示已经把错误响应写给了客户端，调用方应立即返回。
// disposition 为空时使用默认下载处置（attachment）。
func deliverBlobContent(w http.ResponseWriter, r *http.Request, deps Deps, blob blobstore.Blob,
	name, contentType, disposition string, clientDecrypt bool, meta map[string]any,
	purpose blobstore.PresignPurpose) (downloadDelivery, bool) {
	if disposition == "" {
		disposition = "attachment"
	}
	if clientDecrypt {
		if meta != nil {
			setEncryptionHeader(w, meta)
		}
		setContentSHA256Header(w, blob.SHA256)
		reader, err := deps.Blobs.OpenRawWithPurpose(r.Context(), blob, purpose)
		if err != nil {
			writeBusinessError(w, http.StatusInternalServerError, "打开下载文件失败")
			return downloadDelivery{}, false
		}
		return serveBlobStreamWith(w, r, reader, blob.WireSize(), name, contentType, disposition), true
	}
	setContentSHA256Header(w, blob.SHA256)
	reader, err := deps.Blobs.OpenWithPurpose(r.Context(), blob, purpose)
	if err != nil {
		writeBusinessError(w, http.StatusInternalServerError, "打开下载文件失败")
		return downloadDelivery{}, false
	}
	return serveBlobStreamWith(w, r, reader, blob.SizeBytes, name, contentType, disposition), true
}

// serveBlobStream 与旧 serveDownload 等价：以 ReadSeeker 交付，支持 Range/HEAD，
// 返回实际发送区间用于下载计数。
func serveBlobStream(w http.ResponseWriter, r *http.Request, reader io.ReadSeekCloser, size int64, name, contentType string) downloadDelivery {
	return serveBlobStreamWith(w, r, reader, size, name, contentType, "attachment")
}

// serveBlobStreamWith 与 serveBlobStream 相同，但可指定 Content-Disposition
// （attachment=下载，inline=预览）。
func serveBlobStreamWith(w http.ResponseWriter, r *http.Request, reader io.ReadSeekCloser, size int64, name, contentType, disposition string) downloadDelivery {
	defer reader.Close()
	rangeHeader := strings.TrimSpace(r.Header.Get("Range"))
	start, end, trackable := requestedDownloadRange(rangeHeader, size)
	// 多段（bytes=a-b,c-d）或语法非法的 Range 无法判定"是否完整交付"，而
	// http.ServeContent 仍会把全部字节写出——下载次数与流量配额都依赖单段语义，
	// 因此直接拒绝（客户端会自动回退为单段或全量请求），避免计数被绕过。
	if rangeHeader != "" && !trackable {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
		writeBusinessError(w, http.StatusRequestedRangeNotSatisfiable, "不支持的 Range 请求")
		return downloadDelivery{}
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
	counter := &countingResponseWriter{ResponseWriter: w}
	http.ServeContent(counter, r, name, time.Time{}, reader)
	statusOK := counter.status == http.StatusOK || counter.status == http.StatusPartialContent
	return downloadDelivery{start: start, end: end, complete: trackable && statusOK && counter.written == end-start+1}
}

// serveLocalArtifact 交付本地临时制品（ZIP 等）：与 serveBlobStream 相同的
// Range/ServeContent 行为，用于无法表示为存储对象的即时打包产物。
func serveLocalArtifact(w http.ResponseWriter, r *http.Request, path string, size int64, name, contentType string) downloadDelivery {
	f, err := os.Open(path)
	if err != nil {
		writeBusinessError(w, http.StatusInternalServerError, "打开下载文件失败")
		return downloadDelivery{}
	}
	return serveBlobStream(w, r, f, size, name, contentType)
}

// blobContentType 根据资源的 MIME 与文件名推导响应类型。
func blobContentType(item resource.Resource) string {
	if item.MimeType != nil {
		if value := strings.TrimSpace(*item.MimeType); value != "" {
			return value
		}
	}
	if value := mime.TypeByExtension(strings.ToLower(filepath.Ext(item.Name))); value != "" {
		return value
	}
	return "application/octet-stream"
}
