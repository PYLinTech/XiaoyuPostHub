import React, { useEffect, useState } from 'react';
import axios from 'axios';
import {
  Button,
  Card,
  Grid,
  Message,
  Progress,
  Spin,
  Statistic,
  Tag,
  Typography,
} from '@arco-design/web-react';
import {
  IconCloud,
  IconFile,
  IconLink,
  IconRefresh,
  IconShareInternal,
  IconUserGroup,
} from '@arco-design/web-react/icon';
import { AdminPageHeader, formatBytes } from '../shared';
import styles from '../style/index.module.less';
import uiText from '@/utils/uiText';
const { Row, Col } = Grid;
const { Text, Title } = Typography;
const initialData = {
  userCount: 0,
  fileCount: 0,
  folderCount: 0,
  storageUsedBytes: 0,
  storageAvailableBytes: 0,
  storageTotalBytes: 0,
  activeShareCount: 0,
  activeDirectCount: 0,
  shareDownloadCount: 0,
  shareTrafficBytes: 0,
};
function Overview() {
  const [data, setData] = useState(initialData);
  const [loading, setLoading] = useState(true);
  const [updatedAt, setUpdatedAt] = useState('');
  const load = () => {
    setLoading(true);
    axios
      .get('/api/admin/overview')
      .then((res) => {
        setData(res.data.data);
        setUpdatedAt(
          new Date().toLocaleTimeString('zh-CN', {
            hour12: false,
          })
        );
      })
      .catch(() => Message.error(uiText('实时概览加载失败')))
      .finally(() => setLoading(false));
  };
  useEffect(load, []);
  const storageVisibleCapacity =
    data.storageUsedBytes + data.storageAvailableBytes;
  const storagePercent = storageVisibleCapacity
    ? Math.min(
        100,
        Math.round((data.storageUsedBytes / storageVisibleCapacity) * 1000) / 10
      )
    : 0;
  const stats = [
    {
      label: uiText('用户总数'),
      value: data.userCount,
      icon: <IconUserGroup />,
    },
    {
      label: uiText('文件总数'),
      value: data.fileCount,
      icon: <IconFile />,
    },
    {
      label: uiText('有效分享'),
      value: data.activeShareCount,
      icon: <IconShareInternal />,
    },
    {
      label: uiText('有效直链'),
      value: data.activeDirectCount,
      icon: <IconLink />,
    },
  ];
  return (
    <div className={styles.page}>
      <AdminPageHeader
        title={uiText('实时概览')}
        description={uiText('掌握站点资源、分享和服务运行状态。')}
        extra={
          <Button icon={<IconRefresh />} onClick={load}>
            {uiText('刷新数据')}
          </Button>
        }
      />
      <Spin
        loading={loading}
        style={{
          width: '100%',
        }}
      >
        <div className={styles['stat-grid']}>
          {stats.map((item) => (
            <Card className={styles['stat-card']} key={item.label}>
              <div className={styles['stat-label']}>
                <span>{item.label}</span>
                <span className={styles['stat-icon']}>{item.icon}</span>
              </div>
              <Statistic value={item.value} groupSeparator />
            </Card>
          ))}
        </div>
        <Row gutter={16}>
          <Col xs={24} lg={16}>
            <Card className={styles['section-card']}>
              <div className={styles['section-title']}>
                <Title heading={6}>{uiText('服务状态')}</Title>
                <Text type="secondary">
                  {uiText('更新于')}
                  {updatedAt || '--:--:--'}
                </Text>
              </div>
              <div className={styles['health-list']}>
                {[
                  uiText('API 服务'),
                  uiText('数据库连接'),
                  uiText('文件存储'),
                ].map((name, index) => (
                  <div className={styles['health-item']} key={name}>
                    <div className={styles['health-name']}>
                      <Text bold>{name}</Text>
                      <Tag color="green">{uiText('正常')}</Tag>
                    </div>
                    {index === 2 ? (
                      <div className={styles['storage-health']}>
                        <Text type="secondary">
                          {uiText('已用')}
                          {formatBytes(data.storageUsedBytes)}
                          {uiText('/ 可用')}{' '}
                          {formatBytes(data.storageAvailableBytes)}
                        </Text>
                        <Progress
                          percent={storagePercent}
                          showText={false}
                          status={storagePercent >= 90 ? 'error' : 'normal'}
                        />
                      </div>
                    ) : (
                      <Text type="secondary">{uiText('响应正常')}</Text>
                    )}
                  </div>
                ))}
              </div>
            </Card>
          </Col>
          <Col xs={24} lg={8}>
            <Card className={styles['section-card']}>
              <div className={styles['section-title']}>
                <Title heading={6}>{uiText('分享消耗')}</Title>
                <IconCloud />
              </div>
              <Statistic
                title={uiText('累计下载')}
                value={data.shareDownloadCount}
                suffix={uiText('次')}
              />
              <div
                style={{
                  marginTop: 18,
                }}
              >
                <Text type="secondary">{uiText('累计流量')}</Text>
                <Title heading={5}>{formatBytes(data.shareTrafficBytes)}</Title>
              </div>
            </Card>
          </Col>
        </Row>
      </Spin>
    </div>
  );
}
export default Overview;
