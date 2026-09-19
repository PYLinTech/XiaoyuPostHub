package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/PYLinTech/XiaoyuPostHub/backend/admin"
	"github.com/PYLinTech/XiaoyuPostHub/backend/blobstore"
)

// 存储维护任务的后台执行器：每 5 秒从队列领取一个任务，逐项处理并更新进度。
//
// 设计取舍：
//   - 顺序执行（不并发），避免对第三方存储造成压力与触发限流；
//   - 迁移/补加密/分片都遵循「先写新对象 → 更新数据库 → 再删旧对象」的顺序：
//     任何一步失败都不丢数据；旧对象删除失败会让任务项标记失败（可重跑，幂等），
//     而不是留下无法追溯的影子数据；
//   - 单项 panic / 超时都被就地捕获，不会影响服务进程与其它任务；
//   - 进程重启会把 running 任务标记为失败，管理员可重新发起（幂等）。

const (
	storageTaskPollInterval = 5 * time.Second
	// storageTaskItemTimeout 单个对象的处理上限（大对象迁移可能较慢）。
	storageTaskItemTimeout = 30 * time.Minute
	// storageTaskBatchSize 每批领取的待处理对象数量。
	storageTaskBatchSize = 10
)

// RunStorageTasks 启动存储维护任务执行器，随进程 ctx 结束而退出。
func RunStorageTasks(ctx context.Context, repo *admin.Repo, blobs *blobstore.Service) {
	if err := repo.FailInterruptedStorageTasks(ctx); err != nil {
		log.Printf("存储任务：标记中断任务失败时出错：%v", err)
	}
	ticker := time.NewTicker(storageTaskPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runNextStorageTaskSafely(ctx, repo, blobs)
		}
	}
}

// runNextStorageTaskSafely 兜底捕获 panic：后台执行器绝不能把整个服务带崩。
func runNextStorageTaskSafely(ctx context.Context, repo *admin.Repo, blobs *blobstore.Service) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("存储任务：执行器发生内部错误（已恢复）：%v", recovered)
		}
	}()
	runNextStorageTask(ctx, repo, blobs)
}

func runNextStorageTask(ctx context.Context, repo *admin.Repo, blobs *blobstore.Service) {
	task, err := repo.ClaimQueuedStorageTask(ctx)
	if err != nil {
		log.Printf("存储任务：领取任务失败：%v", err)
		return
	}
	if task == nil {
		return
	}
	log.Printf("存储任务 #%d（%s）开始执行，共 %d 项", task.ID, task.Type, task.TotalItems)
	for {
		items, listErr := repo.ListPendingStorageTaskItems(ctx, task.ID, storageTaskBatchSize)
		if listErr != nil {
			_ = repo.FinishStorageTask(ctx, task.ID, "failed", "读取待处理对象失败")
			log.Printf("存储任务 #%d：读取待处理对象失败：%v", task.ID, listErr)
			return
		}
		if len(items) == 0 {
			break
		}
		for _, item := range items {
			status, message, written := processStorageTaskItemSafely(ctx, repo, blobs, *task, item)
			if markErr := repo.MarkStorageTaskItem(ctx, task.ID, item.BlobID, status, message, written); markErr != nil {
				// 进度无法落库时中止本轮：否则同一批对象会被反复重新处理
				// （迁移/补加密会重复完整上传），甚至形成热循环。
				log.Printf("存储任务 #%d：更新对象状态失败，本轮中止：%v", task.ID, markErr)
				_ = repo.FinishStorageTask(ctx, task.ID, "failed", "更新任务进度失败，请重新发起")
				return
			}
		}
	}
	_ = repo.FinishStorageTask(ctx, task.ID, "done", "")
	log.Printf("存储任务 #%d 执行结束", task.ID)
}

// processStorageTaskItemSafely 保证单项的 panic 与超时都不会影响其它对象。
func processStorageTaskItemSafely(ctx context.Context, repo *admin.Repo, blobs *blobstore.Service, task admin.StorageTask, item admin.StorageTaskItem) (status, message string, written int64) {
	itemCtx, cancel := context.WithTimeout(ctx, storageTaskItemTimeout)
	defer cancel()
	defer func() {
		if recovered := recover(); recovered != nil {
			status = "failed"
			message = fmt.Sprintf("处理时发生内部错误：%v", recovered)
			log.Printf("存储任务 #%d：对象 %s 处理发生 panic（已恢复）：%v", task.ID, item.BlobID, recovered)
		}
	}()
	written, err := executeStorageTaskItem(itemCtx, repo, blobs, task, item)
	if err != nil {
		return "failed", err.Error(), 0
	}
	return "done", "", written
}

// executeStorageTaskItem 处理单个对象，返回已处理字节数。
func executeStorageTaskItem(ctx context.Context, repo *admin.Repo, blobs *blobstore.Service, task admin.StorageTask, item admin.StorageTaskItem) (int64, error) {
	blob, err := blobs.Get(ctx, item.BlobID)
	if err != nil {
		// 对象元数据已被清理：若仍留有未清理的旧物理对象，补删后收尾。
		if _, cleanupErr := cleanupRecordedOldRefs(ctx, repo, blobs, task, item); cleanupErr != nil {
			return 0, cleanupErr
		}
		return 0, nil
	}
	// 续跑：上一次执行已在替换元数据后中断（旧对象未清理成功）——先按回溯记录
	// 补删。不能依赖常规幂等判断：元数据已指向新对象，重跑会被"已在目标后端"
	// 跳过，旧对象就再也无人清理。
	if cleaned, cleanupErr := cleanupRecordedOldRefs(ctx, repo, blobs, task, item); cleanupErr != nil {
		return 0, cleanupErr
	} else if cleaned {
		return blob.SizeBytes, nil
	}
	switch task.Type {
	case "migrate":
		target, _ := admin.ParamInt64(task.Params, "target_backend_id")
		if target <= 0 {
			return 0, errNoTargetBackend
		}
		if blob.BackendID == target {
			return blob.SizeBytes, nil // 已在目标后端（重跑时幂等跳过）
		}
		return rewriteAndReplace(ctx, repo, blobs, task.ID, blob, target, blob.Encryption != nil, blob.ChunkSize)
	case "encrypt":
		if blob.Encryption != nil {
			return blob.SizeBytes, nil // 已加密：跳过
		}
		return rewriteAndReplace(ctx, repo, blobs, task.ID, blob, blob.BackendID, true, blob.ChunkSize)
	case "decrypt":
		if blob.Encryption == nil {
			return blob.SizeBytes, nil // 已明文：跳过
		}
		// 读出的始终是明文（Open 自动解信封），以「不加密」重写即完成解密；同样遵循
		// "先写新对象 → 替换元数据 → 删旧对象"，任何一步失败都可安全重跑。
		// 需要加密密钥可用，否则无法解出明文（密钥丢失的加密对象无法还原）。
		return rewriteAndReplace(ctx, repo, blobs, task.ID, blob, blob.BackendID, false, blob.ChunkSize)
	case "chunk":
		target, _ := admin.ParamInt64(task.Params, "chunk_size")
		if target <= 0 {
			return 0, errNoChunkSize
		}
		if int64(blob.ChunkSize) == target {
			return blob.SizeBytes, nil // 已按目标粒度分片
		}
		return rewriteAndReplace(ctx, repo, blobs, task.ID, blob, blob.BackendID, blob.Encryption != nil, int32(target))
	case "purge_orphan":
		if blob.RefCount > 0 {
			// 建任务后被重新引用：跳过，避免误删。
			return 0, nil
		}
		// 顺序：取定位符 → 条件摘除元数据 → 删物理。反序会出现"物理已删、元数据
		// 仍在"的窗口：期间并发 Attach 会让行重新活跃却指向已删除的对象。
		refs, refsErr := blobs.ObjectRefs(ctx, blob)
		if refsErr != nil {
			return 0, refsErr
		}
		dropped, err := blobs.DropObject(ctx, blob.ID)
		if err != nil {
			return 0, err
		}
		if !dropped {
			return 0, nil // 竞态：行被引用，保留元数据（物理对象尚未删除）
		}
		// 元数据已摘除：此时物理删除失败只留下无法追溯的垃圾，不会出现
		// "元数据在、对象已丢"的不可恢复状态。
		if err := blobs.DeletePhysicalRefs(ctx, blob.BackendID, refs); err != nil {
			return 0, fmt.Errorf("孤儿对象物理删除失败（可重新发起清理任务）：%w", err)
		}
		return blob.SizeBytes, nil
	default:
		return 0, nil
	}
}

// rewriteAndReplace 先写新对象、替换数据库定位、再删旧对象。
// 旧对象删除失败时返回错误：任务项会标记失败并可安全重跑（重试会重新写入并
// 再次尝试删除），而不是留下没有任何数据库记录的影子对象。
func rewriteAndReplace(ctx context.Context, repo *admin.Repo, blobs *blobstore.Service, taskID int64, blob blobstore.Blob, backendID int64, encrypt bool, chunkSize int32) (int64, error) {
	// 旧定位符必须在替换前取出：ReplaceObject 之后 blob_parts 已指向新对象，
	// 再按 ID 回查会得到新定位符，用它删除会删错对象（同后端时删掉新写入的对象）。
	oldRefs, err := blobs.ObjectRefs(ctx, blob)
	if err != nil {
		return 0, err
	}
	// 先落回溯记录再替换元数据：元数据一旦指向新对象，旧对象就再无记录可查。
	// 记录后即使中途失败，重跑任务项会按记录补删旧对象。
	if err := repo.MarkStorageTaskItemOldRefs(ctx, taskID, blob.ID, blob.BackendID, oldRefs); err != nil {
		return 0, fmt.Errorf("记录旧对象定位符失败：%w", err)
	}
	replacement, err := blobs.RewriteObject(ctx, blob, backendID, encrypt, chunkSize)
	if err != nil {
		if clearErr := repo.ClearStorageTaskItemOldRefs(ctx, taskID, blob.ID); clearErr != nil {
			log.Printf("存储任务 #%d：清除旧对象回溯记录失败：%v", taskID, clearErr)
		}
		return 0, err
	}
	if err := blobs.ReplaceObject(ctx, blob.ID, replacement); err != nil {
		// 替换未生效：旧对象仍由元数据引用，清除回溯记录（无需补删）。
		if clearErr := repo.ClearStorageTaskItemOldRefs(ctx, taskID, blob.ID); clearErr != nil {
			log.Printf("存储任务 #%d：清除旧对象回溯记录失败：%v", taskID, clearErr)
		}
		// 中转行在写入时即为 orphaned（未被引用），这里主动丢弃；即便清理失败
		// 也会被孤儿清理任务回收。定位符同样要在摘除元数据前取出。
		replacementRefs, refsErr := blobs.ObjectRefs(ctx, replacement)
		if _, dropErr := blobs.DropObject(ctx, replacement.ID); dropErr != nil {
			log.Printf("存储任务：清理中转对象 %s 失败：%v", replacement.ID, dropErr)
		}
		if refsErr == nil {
			_ = blobs.DeletePhysicalRefs(ctx, replacement.BackendID, replacementRefs)
		}
		return 0, err
	}
	if err := blobs.DeletePhysicalRefs(ctx, blob.BackendID, oldRefs); err != nil {
		// 回溯记录保留在任务项上：重跑本任务会先按记录补删旧对象。
		return 0, fmt.Errorf("旧对象清理失败（重跑本任务可自动补删）：%w", err)
	}
	if clearErr := repo.ClearStorageTaskItemOldRefs(ctx, taskID, blob.ID); clearErr != nil {
		// 清除失败不影响正确性：重跑时补删是幂等的（对象已不存在）。
		log.Printf("存储任务 #%d：清除旧对象回溯记录失败：%v", taskID, clearErr)
	}
	return blob.SizeBytes, nil
}

// cleanupRecordedOldRefs 补删「上一次执行遗留的旧物理对象」；返回是否执行了补删。
// record 为空表示没有待清理的旧对象，直接返回 false。
func cleanupRecordedOldRefs(ctx context.Context, repo *admin.Repo, blobs *blobstore.Service, task admin.StorageTask, item admin.StorageTaskItem) (bool, error) {
	if item.OldBackendID == nil || len(item.OldRefs) == 0 {
		return false, nil
	}
	if err := blobs.DeletePhysicalRefs(ctx, *item.OldBackendID, item.OldRefs); err != nil {
		return false, fmt.Errorf("补删上次遗留的旧对象失败：%w", err)
	}
	if err := repo.ClearStorageTaskItemOldRefs(ctx, task.ID, item.BlobID); err != nil {
		return false, fmt.Errorf("清除旧对象回溯记录失败：%w", err)
	}
	log.Printf("存储任务 #%d：对象 %s 已补删上次遗留的旧物理对象（%d 个定位符）",
		task.ID, item.BlobID, len(item.OldRefs))
	return true, nil
}

var (
	errNoTargetBackend = errors.New("迁移任务缺少目标存储后端")
	errNoChunkSize     = errors.New("分片任务缺少分片大小")
)
