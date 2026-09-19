package blobstore

import (
	"context"
	"fmt"
	"io"
	"net/http"
)

// remoteReadSeeker 是「远端对象 + HTTP Range」的通用读取流：支持 Seek，定位到
// 新位置时发起带 Range 的请求；远端忽略 Range（返回 200）时流式丢弃前缀，保证
// 数据边界正确。123 云盘与 S3 兼容后端共用；每次请求前的签名/鉴权由 prepare
// 回调完成（在 Range 头设置之后调用，保证签名覆盖完整请求头）。
type remoteReadSeeker struct {
	ctx     context.Context
	client  *http.Client
	url     string
	size    int64
	pos     int64
	active  io.ReadCloser
	prepare func(req *http.Request) error
}

func (r *remoteReadSeeker) Read(p []byte) (int, error) {
	if r.pos >= r.size {
		return 0, io.EOF
	}
	if r.active == nil {
		if err := r.openAt(r.pos); err != nil {
			return 0, err
		}
	}
	n, err := r.active.Read(p)
	r.pos += int64(n)
	if err == io.EOF {
		_ = r.active.Close()
		r.active = nil
		if r.pos >= r.size {
			err = nil
		} else {
			// 远端提前结束：不能让上层把截断当成正常完成。
			err = io.ErrUnexpectedEOF
		}
	}
	return n, err
}

func (r *remoteReadSeeker) openAt(pos int64) error {
	request, err := http.NewRequestWithContext(r.ctx, http.MethodGet, r.url, nil)
	if err != nil {
		return err
	}
	if pos > 0 {
		request.Header.Set("Range", fmt.Sprintf("bytes=%d-", pos))
	}
	if r.prepare != nil {
		if err := r.prepare(request); err != nil {
			return err
		}
	}
	response, err := r.client.Do(request)
	if err != nil {
		return fmt.Errorf("blobstore: 远端下载失败：%w", err)
	}
	switch response.StatusCode {
	case http.StatusPartialContent:
		r.active = response.Body
		return nil
	case http.StatusOK:
		// 远端忽略 Range：流式丢弃前缀。
		if pos > 0 {
			if _, err := io.CopyN(io.Discard, response.Body, pos); err != nil {
				response.Body.Close()
				return fmt.Errorf("blobstore: 远端下载跳转失败：%w", err)
			}
		}
		r.active = response.Body
		return nil
	default:
		body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		response.Body.Close()
		return fmt.Errorf("blobstore: 远端下载失败（HTTP %d：%s）",
			response.StatusCode, bodyTail(body))
	}
}

func (r *remoteReadSeeker) Seek(offset int64, whence int) (int64, error) {
	target, err := seekTarget(r.pos, r.size, offset, whence)
	if err != nil {
		return 0, err
	}
	if target != r.pos {
		if r.active != nil {
			_ = r.active.Close()
			r.active = nil
		}
		r.pos = target
	}
	return target, nil
}

func (r *remoteReadSeeker) Close() error {
	if r.active != nil {
		err := r.active.Close()
		r.active = nil
		return err
	}
	return nil
}

// bodyTail 截断错误响应体，避免把大段 HTML 拼进错误信息。
func bodyTail(body []byte) string {
	const limit = 200
	if len(body) > limit {
		return string(body[:limit]) + "..."
	}
	return string(body)
}
