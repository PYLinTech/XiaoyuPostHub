import uiText from '@/utils/uiText';

export interface UploadTask {
  id: string;
  filename: string;
  parentId?: string;
  totalSize: number;
  chunkSize: number;
  totalChunks: number;
  receivedChunks: number[];
  sha256: string;
  status: string;
  errorMessage?: string;
  queuePosition?: number;
  progress?: number;
  local?: boolean;
  needsFile?: boolean;
}

export interface UploadConfig {
  taskChunkConcurrency: number;
  userTaskConcurrency: number;
}

export const DEFAULT_CONFIG: UploadConfig = {
  taskChunkConcurrency: 3,
  userTaskConcurrency: 2,
};

export const MAX_PERSISTED_FILE_SIZE = 512 * 1024 * 1024;

export type ConflictAction = 'overwrite' | 'skip' | 'auto_rename';

export interface UploadConflict {
  index: number;
  filename: string;
  action: ConflictAction;
}

export function taskPercent(task: UploadTask) {
  if (task.status === 'completed') return 100;
  if (task.progress != null) return Math.max(0, Math.min(99, task.progress));
  if (!task.totalChunks) return 0;
  const receivedCount = Array.isArray(task.receivedChunks)
    ? task.receivedChunks.length
    : 0;
  return Math.round((receivedCount * 100) / task.totalChunks);
}

export function normalizeTask(task: UploadTask): UploadTask {
  return {
    ...task,
    receivedChunks: Array.isArray(task.receivedChunks)
      ? task.receivedChunks
      : [],
  };
}

export function overallPercent(tasks: UploadTask[]) {
  if (!tasks.length) return 0;
  const totalWeight = tasks.reduce(
    (sum, task) => sum + Math.max(1, task.totalSize),
    0
  );
  const uploadedWeight = tasks.reduce(
    (sum, task) =>
      sum + (Math.max(1, task.totalSize) * taskPercent(task)) / 100,
    0
  );
  return Math.round((uploadedWeight * 100) / totalWeight);
}

export function taskStatus(task: UploadTask) {
  if (task.needsFile) return uiText('等待重新选择文件');
  const labels = {
    hashing: uiText('正在校验文件'),
    queued: uiText('等待上传'),
    uploading: uiText('正在上传'),
    paused: uiText('已暂停'),
    completing: uiText('正在合并分片'),
    completed: uiText('上传完成'),
    failed: uiText('上传失败'),
    canceled: uiText('已取消'),
  };
  return labels[task.status] || task.status;
}

export function canQueue(task: UploadTask) {
  return (
    !task.local &&
    !['completed', 'canceled', 'completing'].includes(task.status)
  );
}
