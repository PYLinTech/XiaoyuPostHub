import React, {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
} from 'react';
import axios from 'axios';
import {
  Button,
  Message,
  Progress,
  Space,
  Typography,
} from '@arco-design/web-react';
import { GlobalContext } from '@/context';
import { blobSHA256, fileSHA256 } from '@/utils/sha256';
import {
  loadUploadFile,
  removeUploadFile,
  saveUploadFile,
} from '@/utils/uploadFiles';
import uiText, { uiServerText } from '@/utils/uiText';
import styles from './index.module.less';

interface UploadTask {
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

interface UploadConfig {
  taskChunkConcurrency: number;
  userTaskConcurrency: number;
}

interface UploadContextValue {
  addFiles: (files: File[], parentId?: string) => Promise<void>;
}

const UploadContext = createContext<UploadContextValue>({
  addFiles: async () => undefined,
});

const DEFAULT_CONFIG: UploadConfig = {
  taskChunkConcurrency: 3,
  userTaskConcurrency: 2,
};
const MAX_PERSISTED_FILE_SIZE = 512 * 1024 * 1024;

export function useUploadManager() {
  return useContext(UploadContext);
}

function taskPercent(task: UploadTask) {
  if (task.status === 'completed') return 100;
  if (task.progress != null) return Math.max(0, Math.min(99, task.progress));
  if (!task.totalChunks) return 0;
  return Math.round((task.receivedChunks.length * 100) / task.totalChunks);
}

function overallPercent(tasks: UploadTask[]) {
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

function taskStatus(task: UploadTask) {
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

function canQueue(task: UploadTask) {
  return (
    !task.local &&
    !['completed', 'canceled', 'completing'].includes(task.status)
  );
}

export function UploadProvider({ children }: { children: React.ReactNode }) {
  const { userInfo } = useContext(GlobalContext);
  const ownerId = userInfo?.id;
  const [tasks, setTasks] = useState<UploadTask[]>([]);
  const [config, setConfig] = useState<UploadConfig>(DEFAULT_CONFIG);
  const [collapsed, setCollapsed] = useState(false);
  const [schedulerTick, setSchedulerTick] = useState(0);
  const running = useRef(new Set<string>());
  const polling = useRef(new Set<string>());
  const blocked = useRef(new Set<string>());
  const activeOwner = useRef(ownerId);
  const controllers = useRef(new Map<string, AbortController>());
  const fileCache = useRef(new Map<string, File>());
  const replaceFileTarget = useRef<UploadTask>();
  const fileInput = useRef<HTMLInputElement>();

  const updateTask = useCallback((id: string, patch: Partial<UploadTask>) => {
    setTasks((current) =>
      current.map((task) => (task.id === id ? { ...task, ...patch } : task))
    );
  }, []);

  const notifyCompleted = useCallback((task: UploadTask) => {
    window.dispatchEvent(
      new CustomEvent('xph-upload-completed', {
        detail: { parentId: task.parentId || null },
      })
    );
  }, []);

  const runTask = useCallback(
    async (task: UploadTask, file: File) => {
      if (
        !ownerId ||
        running.current.has(task.id) ||
        blocked.current.has(task.id)
      )
        return;
      running.current.add(task.id);
      const controller = new AbortController();
      controllers.current.set(task.id, controller);
      try {
        updateTask(task.id, {
          status: 'uploading',
          needsFile: false,
          errorMessage: '',
        });
        const received = new Set(task.receivedChunks || []);
        const pending = Array.from(
          { length: task.totalChunks },
          (_, index) => index
        ).filter((index) => !received.has(index));
        let nextPending = 0;
        let completedBytes = Array.from(received).reduce((sum, index) => {
          const start = index * task.chunkSize;
          return (
            sum +
            Math.max(0, Math.min(file.size, start + task.chunkSize) - start)
          );
        }, 0);
        const inFlight = new Map<number, number>();
        const reportProgress = () => {
          const sending = Array.from(inFlight.values()).reduce(
            (sum, value) => sum + value,
            0
          );
          updateTask(task.id, {
            progress: file.size
              ? Math.round(((completedBytes + sending) * 100) / file.size)
              : 99,
          });
        };
        const worker = async () => {
          while (
            nextPending < pending.length &&
            !blocked.current.has(task.id) &&
            !controller.signal.aborted
          ) {
            const index = pending[nextPending];
            nextPending += 1;
            const start = index * task.chunkSize;
            const end = Math.min(file.size, start + task.chunkSize);
            const chunk = file.slice(start, end);
            const checksum = await blobSHA256(chunk);
            await axios.put(`/api/uploads/${task.id}/chunks/${index}`, chunk, {
              signal: controller.signal,
              headers: {
                'Content-Type': 'application/octet-stream',
                'X-Chunk-SHA256': checksum,
              },
              onUploadProgress: (event) => {
                inFlight.set(index, Math.min(event.loaded, chunk.size));
                reportProgress();
              },
            });
            inFlight.delete(index);
            completedBytes += chunk.size;
            received.add(index);
            updateTask(task.id, {
              receivedChunks: Array.from(received).sort((a, b) => a - b),
            });
            reportProgress();
          }
        };
        const workerCount = Math.max(
          1,
          Math.min(config.taskChunkConcurrency, pending.length || 1)
        );
        const workers = Array.from({ length: workerCount }, () => worker());
        try {
          await Promise.all(workers);
        } catch (error) {
          controller.abort();
          await Promise.allSettled(workers);
          throw error;
        }
        if (blocked.current.has(task.id) || controller.signal.aborted) return;
        updateTask(task.id, { status: 'completing', progress: 99 });
        const response = await axios.post(`/api/uploads/${task.id}/complete`);
        const completed: UploadTask = {
          ...task,
          ...response.data.task,
          status: 'completed',
          progress: 100,
          receivedChunks: Array.from(received),
        };
        updateTask(task.id, completed);
        fileCache.current.delete(task.id);
        await removeUploadFile(ownerId, task.id).catch(() => undefined);
        notifyCompleted(completed);
      } catch (error) {
        if (axios.isCancel(error) || error?.code === 'ERR_CANCELED') return;
        updateTask(task.id, {
          status: 'failed',
          errorMessage: error?.response?.data?.msg || uiText('上传失败'),
        });
      } finally {
        running.current.delete(task.id);
        controllers.current.delete(task.id);
        setSchedulerTick((value) => value + 1);
      }
    },
    [config.taskChunkConcurrency, notifyCompleted, ownerId, updateTask]
  );

  const restoreTasks = useCallback(async () => {
    if (!ownerId) return;
    try {
      const [configResponse, taskResponse] = await Promise.all([
        axios.get('/api/uploads/config'),
        axios.get('/api/uploads'),
      ]);
      if (activeOwner.current !== ownerId) return;
      setConfig({
        taskChunkConcurrency:
          configResponse.data.taskChunkConcurrency ||
          DEFAULT_CONFIG.taskChunkConcurrency,
        userTaskConcurrency:
          configResponse.data.userTaskConcurrency ||
          DEFAULT_CONFIG.userTaskConcurrency,
      });
      const serverTasks: UploadTask[] = taskResponse.data.items || [];
      const restored = await Promise.all(
        serverTasks.map(async (task) => {
          if (task.status === 'completed' || task.status === 'canceled')
            return task;
          const stored = await loadUploadFile(ownerId, task.id).catch(
            () => undefined
          );
          if (stored?.file) fileCache.current.set(task.id, stored.file);
          return { ...task, needsFile: !stored?.file };
        })
      );
      if (activeOwner.current !== ownerId) return;
      setTasks(restored);
    } catch {
      Message.error(uiText('上传任务加载失败'));
    }
  }, [ownerId]);

  useEffect(() => {
    activeOwner.current = ownerId;
    blocked.current.clear();
    running.current.clear();
    polling.current.clear();
    controllers.current.forEach((controller) => controller.abort());
    controllers.current.clear();
    fileCache.current.clear();
    setTasks([]);
    setConfig(DEFAULT_CONFIG);
    if (ownerId) restoreTasks();
  }, [ownerId, restoreTasks]);

  useEffect(() => {
    const applyConfig = (event: Event) => {
      const detail = (event as CustomEvent<Partial<UploadConfig>>).detail;
      setConfig((current) => ({ ...current, ...detail }));
    };
    window.addEventListener('xph-upload-config-updated', applyConfig);
    return () =>
      window.removeEventListener('xph-upload-config-updated', applyConfig);
  }, []);

  useEffect(() => {
    const available = Math.max(
      0,
      config.userTaskConcurrency - running.current.size
    );
    if (!available) return;
    tasks
      .filter(
        (task) =>
          ['queued', 'uploading'].includes(task.status) &&
          !task.needsFile &&
          !blocked.current.has(task.id) &&
          !running.current.has(task.id) &&
          fileCache.current.has(task.id)
      )
      .slice(0, available)
      .forEach((task) => {
        const file = fileCache.current.get(task.id);
        if (file) runTask(task, file);
      });
  }, [config.userTaskConcurrency, runTask, schedulerTick, tasks]);

  useEffect(() => {
    const completing = tasks.filter(
      (task) => task.status === 'completing' && !polling.current.has(task.id)
    );
    completing.forEach((task) => {
      polling.current.add(task.id);
      window.setTimeout(async () => {
        try {
          const response = await axios.get(`/api/uploads/${task.id}`);
          const latest: UploadTask = response.data.task;
          updateTask(task.id, latest);
          if (latest.status === 'completed') {
            fileCache.current.delete(task.id);
            if (ownerId)
              await removeUploadFile(ownerId, task.id).catch(() => undefined);
            notifyCompleted(latest);
          }
        } catch {
          updateTask(task.id, {
            status: 'failed',
            errorMessage: uiText('读取上传任务失败'),
          });
        } finally {
          polling.current.delete(task.id);
          setSchedulerTick((value) => value + 1);
        }
      }, 1500);
    });
  }, [notifyCompleted, ownerId, schedulerTick, tasks, updateTask]);

  const addFiles = useCallback(
    async (files: File[], parentId?: string) => {
      if (!ownerId) return;
      const batchId = `batch-${Date.now()}-${Math.random()
        .toString(36)
        .slice(2)}`;
      setCollapsed(false);
      for (const file of files) {
        const localId = `local-${Date.now()}-${Math.random()}`;
        const localTask: UploadTask = {
          id: localId,
          filename: file.name,
          parentId,
          totalSize: file.size,
          chunkSize: 0,
          totalChunks: 0,
          receivedChunks: [],
          sha256: '',
          status: 'hashing',
          progress: 0,
          local: true,
        };
        fileCache.current.set(localId, file);
        setTasks((current) => [...current, localTask]);
        try {
          const checksum = await fileSHA256(file, (value) =>
            updateTask(localId, { progress: Math.round(value * 100) })
          );
          if (blocked.current.has(localId) || activeOwner.current !== ownerId) {
            fileCache.current.delete(localId);
            continue;
          }
          const response = await axios.post('/api/uploads', {
            batchId,
            filename: file.name,
            parentId: parentId || null,
            size: file.size,
            mimeType: file.type,
            sha256: checksum,
          });
          const task: UploadTask = response.data.task;
          fileCache.current.delete(localId);
          setTasks((current) =>
            current
              .filter((item) => item.id !== task.id)
              .map((item) =>
                item.id === localId
                  ? {
                      ...task,
                      progress: response.data.instant ? 100 : undefined,
                    }
                  : item
              )
          );
          if (response.data.instant) {
            notifyCompleted(task);
            continue;
          }
          fileCache.current.set(task.id, file);
          if (file.size <= MAX_PERSISTED_FILE_SIZE) {
            await saveUploadFile(ownerId, task.id, checksum, file).catch(
              () => undefined
            );
          }
          blocked.current.delete(task.id);
          setSchedulerTick((value) => value + 1);
        } catch (error) {
          updateTask(localId, {
            status: 'failed',
            errorMessage: error?.response?.data?.msg || uiText('上传失败'),
          });
        }
      }
    },
    [notifyCompleted, ownerId, updateTask]
  );

  const pauseTask = async (task: UploadTask) => {
    blocked.current.add(task.id);
    controllers.current.get(task.id)?.abort();
    updateTask(task.id, { status: 'paused' });
    if (task.local) return;
    await axios
      .patch(`/api/uploads/${task.id}`, { action: 'pause' })
      .catch(() => {
        updateTask(task.id, {
          status: 'failed',
          errorMessage: uiText('暂停上传失败'),
        });
      });
  };

  const resumeTask = async (task: UploadTask) => {
    if (!ownerId || task.local) return;
    const stored = await loadUploadFile(ownerId, task.id).catch(
      () => undefined
    );
    const file = fileCache.current.get(task.id) || stored?.file;
    if (!file) {
      replaceFileTarget.current = task;
      fileInput.current?.click();
      return;
    }
    blocked.current.delete(task.id);
    try {
      await axios.patch(`/api/uploads/${task.id}`, { action: 'resume' });
      const response = await axios.get(`/api/uploads/${task.id}`);
      fileCache.current.set(task.id, file);
      updateTask(task.id, { ...response.data.task, needsFile: false });
      setSchedulerTick((value) => value + 1);
    } catch (error) {
      updateTask(task.id, {
        status: 'failed',
        errorMessage: error?.response?.data?.msg || uiText('继续上传失败'),
      });
    }
  };

  const deleteTask = async (task: UploadTask) => {
    blocked.current.add(task.id);
    controllers.current.get(task.id)?.abort();
    if (!task.local) {
      try {
        await axios.delete(`/api/uploads/${task.id}`);
      } catch (error) {
        updateTask(task.id, {
          errorMessage:
            error?.response?.data?.msg || uiText('删除上传任务失败'),
        });
        return;
      }
      if (ownerId)
        await removeUploadFile(ownerId, task.id).catch(() => undefined);
    }
    fileCache.current.delete(task.id);
    setTasks((current) => current.filter((item) => item.id !== task.id));
    setSchedulerTick((value) => value + 1);
  };

  const moveTask = async (task: UploadTask, direction: -1 | 1) => {
    const previous = tasks;
    const activeIndexes = tasks
      .map((item, index) => (canQueue(item) ? index : -1))
      .filter((index) => index >= 0);
    const activePosition = activeIndexes.indexOf(tasks.indexOf(task));
    const targetPosition = activePosition + direction;
    if (
      activePosition < 0 ||
      targetPosition < 0 ||
      targetPosition >= activeIndexes.length
    )
      return;
    const sourceIndex = activeIndexes[activePosition];
    const targetIndex = activeIndexes[targetPosition];
    setTasks((current) => {
      const reordered = [...current];
      [reordered[sourceIndex], reordered[targetIndex]] = [
        reordered[targetIndex],
        reordered[sourceIndex],
      ];
      return reordered;
    });
    if (task.local) return;
    try {
      await axios.patch(`/api/uploads/${task.id}`, {
        action: direction < 0 ? 'move_up' : 'move_down',
      });
    } catch {
      setTasks(previous);
      Message.error(uiText('调整上传队列失败'));
    }
  };

  const replaceFile = async (file?: File) => {
    const task = replaceFileTarget.current;
    replaceFileTarget.current = undefined;
    if (!file || !task || !ownerId) return;
    if (file.name !== task.filename || file.size !== task.totalSize) {
      Message.error(uiText('请选择原始上传文件'));
      return;
    }
    const checksum = await fileSHA256(file);
    if (checksum !== task.sha256) {
      Message.error(uiText('所选文件与上传任务不匹配'));
      return;
    }
    fileCache.current.set(task.id, file);
    if (file.size <= MAX_PERSISTED_FILE_SIZE) {
      await saveUploadFile(ownerId, task.id, checksum, file).catch(
        () => undefined
      );
    }
    updateTask(task.id, { needsFile: false });
    await resumeTask(task);
  };

  const progress = useMemo(() => overallPercent(tasks), [tasks]);
  const activeIndexes = useMemo(
    () =>
      tasks
        .map((task, index) => (canQueue(task) ? index : -1))
        .filter((index) => index >= 0),
    [tasks]
  );

  return (
    <UploadContext.Provider value={{ addFiles }}>
      {children}
      <input
        ref={fileInput}
        type="file"
        hidden
        onChange={(event) => {
          replaceFile(event.target.files?.[0]);
          event.target.value = '';
        }}
      />
      {tasks.length > 0 && collapsed && (
        <button
          type="button"
          className={styles.capsule}
          style={
            {
              '--upload-progress': `${progress}%`,
            } as React.CSSProperties
          }
          aria-label={`${uiText('展开上传任务')} ${progress}%`}
          onClick={() => setCollapsed(false)}
        >
          <span>{progress}%</span>
        </button>
      )}
      {tasks.length > 0 && !collapsed && (
        <aside className={styles.panel} aria-label={uiText('上传任务')}>
          <div className={styles.header}>
            <div>
              <strong>{uiText('上传任务')}</strong>
              <span>
                {tasks.filter((task) => task.status === 'completed').length}/
                {tasks.length} · {progress}%
              </span>
            </div>
            <Button size="mini" type="text" onClick={() => setCollapsed(true)}>
              {uiText('折叠')}
            </Button>
          </div>
          <div className={styles.list}>
            {tasks.map((task, index) => {
              const queueIndex = activeIndexes.indexOf(index);
              return (
                <div className={styles.task} key={task.id}>
                  <div className={styles['task-heading']}>
                    <Typography.Text ellipsis>{task.filename}</Typography.Text>
                    <span>{taskPercent(task)}%</span>
                  </div>
                  <Progress
                    percent={taskPercent(task)}
                    showText={false}
                    status={task.status === 'failed' ? 'error' : 'normal'}
                    size="small"
                  />
                  <div className={styles['task-footer']}>
                    <Typography.Text
                      type={task.status === 'failed' ? 'error' : 'secondary'}
                      ellipsis
                    >
                      {task.errorMessage
                        ? uiServerText(task.errorMessage)
                        : taskStatus(task)}
                    </Typography.Text>
                    <Space size={2} className={styles.actions}>
                      {canQueue(task) && (
                        <>
                          <Button
                            size="mini"
                            type="text"
                            disabled={queueIndex <= 0}
                            aria-label={uiText('上移')}
                            onClick={() => moveTask(task, -1)}
                          >
                            ↑
                          </Button>
                          <Button
                            size="mini"
                            type="text"
                            disabled={
                              queueIndex < 0 ||
                              queueIndex >= activeIndexes.length - 1
                            }
                            aria-label={uiText('下移')}
                            onClick={() => moveTask(task, 1)}
                          >
                            ↓
                          </Button>
                        </>
                      )}
                      {['queued', 'uploading'].includes(task.status) && (
                        <Button
                          size="mini"
                          type="text"
                          onClick={() => pauseTask(task)}
                        >
                          {uiText('暂停')}
                        </Button>
                      )}
                      {['paused', 'failed'].includes(task.status) &&
                        !task.local && (
                          <Button
                            size="mini"
                            type="text"
                            onClick={() => resumeTask(task)}
                          >
                            {task.needsFile
                              ? uiText('选择文件')
                              : uiText('继续')}
                          </Button>
                        )}
                      {task.status !== 'completing' && (
                        <Button
                          size="mini"
                          type="text"
                          status="danger"
                          onClick={() => deleteTask(task)}
                        >
                          {uiText('删除')}
                        </Button>
                      )}
                    </Space>
                  </div>
                </div>
              );
            })}
          </div>
        </aside>
      )}
    </UploadContext.Provider>
  );
}
