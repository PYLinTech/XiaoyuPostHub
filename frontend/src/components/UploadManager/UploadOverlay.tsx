import React, { useMemo } from 'react';
import {
  Button,
  Modal,
  Progress,
  Space,
  Typography,
} from '@arco-design/web-react';
import uiText, { uiServerText } from '@/utils/uiText';
import LiquidCapsuleProgress from './LiquidCapsuleProgress';
import styles from './overlay.module.less';
import { canQueue, overallPercent, taskPercent, taskStatus } from './shared';
import type { ConflictAction, UploadConflict, UploadTask } from './shared';

export interface UploadOverlayProps {
  tasks: UploadTask[];
  collapsed: boolean;
  conflicts: UploadConflict[];
  conflictPath: string;
  conflictVisible: boolean;
  onCollapsedChange: (collapsed: boolean) => void;
  onPause: (task: UploadTask) => void;
  onResume: (task: UploadTask) => void;
  onDelete: (task: UploadTask) => void;
  onMove: (task: UploadTask, direction: -1 | 1) => void;
  onConflictActionChange: (index: number, action: ConflictAction) => void;
  onConflictCancel: () => void;
  onConflictConfirm: () => void;
}

/**
 * 上传面板的展示层：冲突弹窗、折叠胶囊与任务列表。
 * 由 UploadProvider 在存在任务或冲突时按需加载。
 */
export default function UploadOverlay({
  tasks,
  collapsed,
  conflicts,
  conflictPath,
  conflictVisible,
  onCollapsedChange,
  onPause,
  onResume,
  onDelete,
  onMove,
  onConflictActionChange,
  onConflictCancel,
  onConflictConfirm,
}: UploadOverlayProps) {
  const progress = useMemo(() => overallPercent(tasks), [tasks]);
  const activeIndexes = useMemo(
    () =>
      tasks
        .map((task, index) => (canQueue(task) ? index : -1))
        .filter((index) => index >= 0),
    [tasks]
  );

  return (
    <>
      <Modal
        title={uiText('处理同名文件')}
        visible={conflictVisible}
        style={{ width: 760, maxWidth: 'calc(100vw - 32px)' }}
        okText={uiText('继续上传')}
        cancelText={uiText('取消本次上传')}
        autoFocus={false}
        onCancel={onConflictCancel}
        onOk={onConflictConfirm}
        unmountOnExit
      >
        <Typography.Paragraph type="secondary">
          {uiText('以下文件与待上传目录中的现有文件同名，请分别选择处理方式。')}
        </Typography.Paragraph>
        <div className={styles['conflict-list']}>
          <div className={styles['conflict-list-header']}>
            <span>{uiText('冲突文件名')}</span>
            <span>{uiText('待上传路径')}</span>
            <span>{uiText('处理方式')}</span>
          </div>
          {conflicts.map((item) => (
            <div
              className={styles['conflict-row']}
              key={`${item.index}-${item.filename}`}
            >
              <span className={styles['conflict-name']} title={item.filename}>
                {item.filename}
              </span>
              <span
                className={styles['conflict-path']}
                title={conflictPath || '/'}
              >
                <span className={styles['conflict-path-label']}>
                  {uiText('待上传路径')}：
                </span>
                {conflictPath || '/'}
              </span>
              <Space size={4} className={styles['conflict-actions']}>
                {(['overwrite', 'skip', 'auto_rename'] as ConflictAction[]).map(
                  (action) => (
                    <Button
                      key={action}
                      size="small"
                      type={item.action === action ? 'primary' : 'secondary'}
                      onClick={() =>
                        onConflictActionChange(item.index, action)
                      }
                    >
                      {uiText(
                        action === 'overwrite'
                          ? '覆盖'
                          : action === 'skip'
                          ? '跳过'
                          : '自动重命名'
                      )}
                    </Button>
                  )
                )}
              </Space>
            </div>
          ))}
        </div>
      </Modal>
      {tasks.length > 0 && collapsed && (
        <LiquidCapsuleProgress
          progress={progress}
          ariaLabel={`${uiText('展开上传任务')} ${progress}%`}
          onClick={() => onCollapsedChange(false)}
        />
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
            <Button
              size="mini"
              type="text"
              onClick={() => onCollapsedChange(true)}
            >
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
                            onClick={() => onMove(task, -1)}
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
                            onClick={() => onMove(task, 1)}
                          >
                            ↓
                          </Button>
                        </>
                      )}
                      {['queued', 'uploading'].includes(task.status) && (
                        <Button
                          size="mini"
                          type="text"
                          onClick={() => onPause(task)}
                        >
                          {uiText('暂停')}
                        </Button>
                      )}
                      {['paused', 'failed'].includes(task.status) &&
                        !task.local && (
                          <Button
                            size="mini"
                            type="text"
                            onClick={() => onResume(task)}
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
                          onClick={() => onDelete(task)}
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
    </>
  );
}
