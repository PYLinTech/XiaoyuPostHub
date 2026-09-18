import type { AxiosRequestConfig } from 'axios';
import axios from './client';

/**
 * 全部后端接口的集中定义。业务代码只从这里取用，不再自行拼接 URL，
 * 便于统一检查接口对应关系与参数形态。
 *
 * 约定：函数原样返回 axios 响应，调用方继续读取 response.data。
 */

/* ------------------------------ 站点与健康 ------------------------------ */

export const fetchSiteConfig = () => axios.get('/api/site-config');

/* -------------------------------- 账户 -------------------------------- */

export const login = (payload: Record<string, unknown>) =>
  axios.post('/api/user/login', payload);

export const loginWithTotp = (payload: Record<string, unknown>) => axios.post('/api/user/login/totp', payload);

export const register = (payload: Record<string, unknown>) => axios.post('/api/user/register', payload);

export const fetchRegistrationSettings = () =>
  axios.get('/api/user/registration-settings');

export const fetchUserInfo = () => axios.get('/api/user/userInfo');

export const logout = () => axios.post('/api/user/logout');

export const fetchTotpStatus = () => axios.get('/api/user/totp');

export const beginTotpSetup = () => axios.post('/api/user/totp/begin');

export const confirmTotpSetup = (payload: Record<string, unknown>) =>
  axios.post('/api/user/totp/confirm', payload);

/* ------------------------------ 站内消息 ------------------------------ */

export const fetchMessages = (config?: AxiosRequestConfig) =>
  axios.get('/api/messages', config);

export const markMessagesRead = (payload: Record<string, unknown>) => axios.post('/api/messages/read', payload);

export const deleteMessages = (payload: Record<string, unknown>) =>
  axios.post('/api/messages/delete', payload);

/* -------------------------------- 上传 -------------------------------- */

export const fetchUploadConfig = () => axios.get('/api/uploads/config');

export const checkUploadConflicts = (payload: Record<string, unknown>) => axios.post('/api/uploads/conflicts', payload);

export const fetchUploadTasks = () => axios.get('/api/uploads');

export const createUploadTask = (payload: Record<string, unknown>) =>
  axios.post('/api/uploads', payload);

export const fetchUploadTask = (id: string) => axios.get(`/api/uploads/${id}`);

export const updateUploadTask = (
  id: string,
  payload: Record<string, unknown>,
  config?: AxiosRequestConfig
) => axios.patch(`/api/uploads/${id}`, payload, config);

export const deleteUploadTask = (id: string) =>
  axios.delete(`/api/uploads/${id}`);

export const uploadChunk = (
  id: string,
  index: number,
  chunk: Blob,
  config?: AxiosRequestConfig
) => axios.put(`/api/uploads/${id}/chunks/${index}`, chunk, config);

export const completeUploadTask = (id: string) =>
  axios.post(`/api/uploads/${id}/complete`);

/* ------------------------------ 文件与目录 ------------------------------ */

export const fetchResourceList = (config?: AxiosRequestConfig) =>
  axios.get('/api/resources', config);

export const createFolder = (payload: Record<string, unknown>) => axios.post('/api/resources/folders', payload);

export const renameResource = (id: string, payload: Record<string, unknown>) =>
  axios.put(`/api/resources/${id}`, payload);

export const deleteResource = (id: string) =>
  axios.delete(`/api/resources/${id}`);

export const downloadResources = (
  payload: Record<string, unknown>,
  config?: AxiosRequestConfig
) => axios.post('/api/resources', payload, config);

/** 预览地址由后端校验鉴权，这里只负责生成给预览组件使用的 URL。 */
export const resourcePreviewUrl = (id: string) =>
  `/api/resources/${encodeURIComponent(id)}/preview`;

/* -------------------------------- 回收站 ------------------------------- */

export const fetchTrash = () => axios.get('/api/trash');

export const restoreTrashItem = (id: string) =>
  axios.post(`/api/trash/${id}/restore`);

export const deleteTrashItem = (id: string) => axios.delete(`/api/trash/${id}`);

export const emptyTrash = () => axios.delete('/api/trash');

/* ------------------------------ 分享与直链 ------------------------------ */

export const fetchShares = () => axios.get('/api/shares');

export const createShare = (payload: Record<string, unknown>) =>
  axios.post('/api/shares', payload);

export const batchManageShares = (payload: Record<string, unknown>) => axios.post('/api/shares/manage', payload);

export const updateShare = (id: number, payload: Record<string, unknown>) =>
  axios.put(`/api/shares/manage/${id}`, payload);

export const fetchDirectLinks = () => axios.get('/api/direct-links');

export const createDirectLink = (payload: Record<string, unknown>) =>
  axios.post('/api/direct-links', payload);

export const batchManageDirectLinks = (payload: Record<string, unknown>) => axios.post('/api/direct-links/manage', payload);

export const updateDirectLink = (
  id: number,
  payload: Record<string, unknown>
) => axios.put(`/api/direct-links/manage/${id}`, payload);

/** 公开分享页：无需登录即可读取分享元数据与内容。 */
export const fetchShareByToken = (token: string) =>
  axios.get(`/api/shares/${encodeURIComponent(token)}`);

export const fetchSharePreview = (token: string, config?: AxiosRequestConfig) =>
  axios.get(`/api/shares/${encodeURIComponent(token)}/preview`, config);

export const fetchPickup = (code: string) =>
  axios.get(`/api/pickups/${encodeURIComponent(code)}`);

/* ------------------------------ 管理后台 ------------------------------ */

export const fetchAdminOverview = () => axios.get('/api/admin/overview');

export const fetchAdminAudit = (limit = 100) =>
  axios.get('/api/admin/audit', { params: { limit } });

export const fetchAdminUsers = () => axios.get('/api/admin/users');

export const setUserGroups = (id: number, payload: Record<string, unknown>) =>
  axios.put(`/api/admin/users/${id}/groups`, payload);

export const resetUserPassword = (id: number, payload: Record<string, unknown>) =>
  axios.put(`/api/admin/users/${id}/password`, payload);

export const setUserDisabled = (id: number, payload: Record<string, unknown>) =>
  axios.put(`/api/admin/users/${id}/disabled`, payload);

export const deleteUser = (id: number) => axios.delete(`/api/admin/users/${id}`);

export const createUserGroup = (payload: Record<string, unknown>) =>
  axios.post('/api/admin/user-groups', payload);

export const updateUserGroup = (id: number, payload: Record<string, unknown>) =>
  axios.put(`/api/admin/user-groups/${id}`, payload);

export const deleteUserGroup = (id: number) =>
  axios.delete(`/api/admin/user-groups/${id}`);

export const setUserGroupMembers = (id: number, payload: Record<string, unknown>) =>
  axios.put(`/api/admin/user-groups/${id}/members`, payload);

export const fetchAdminAccess = () => axios.get('/api/admin/access');

export const createQuotaProfile = (payload: Record<string, unknown>) =>
  axios.post('/api/admin/access/quotas', payload);

export const updateQuotaProfile = (id: number, payload: Record<string, unknown>) =>
  axios.put(`/api/admin/access/quotas/${id}`, payload);

export const deleteQuotaProfile = (id: number) =>
  axios.delete(`/api/admin/access/quotas/${id}`);

export const setGroupQuota = (id: number, payload: Record<string, unknown>) =>
  axios.put(`/api/admin/access/groups/${id}/quota`, payload);

export const setGroupPermissions = (
  id: number,
  payload: Record<string, unknown>
) => axios.put(`/api/admin/access/groups/${id}/permissions`, payload);

export const fetchInvitations = () => axios.get('/api/admin/invitations');

export const issueInvitations = (payload: Record<string, unknown>) =>
  axios.post('/api/admin/invitations', payload);

export const updateInvitationSettings = (payload: Record<string, unknown>) => axios.put('/api/admin/invitations/settings', payload);

export const revokeInvitation = (id: number) =>
  axios.delete(`/api/admin/invitations/${id}`);

export const fetchFileReviews = (config?: AxiosRequestConfig) =>
  axios.get('/api/admin/reviews/files', config);

export const fetchShareReviews = (config?: AxiosRequestConfig) =>
  axios.get('/api/admin/reviews/shares', config);

export const reviewResources = (
  kind: 'files' | 'shares',
  payload: Record<string, unknown>
) => axios.put(`/api/admin/reviews/${kind}`, payload);

export const downloadReviewedFiles = (
  payload: Record<string, unknown>,
  config?: AxiosRequestConfig
) => axios.post('/api/admin/reviews/files/download', payload, config);

export const fetchReviewedTrash = () =>
  axios.get('/api/admin/reviews/files/trash');

export const deleteReviewedTrashItem = (id: string) =>
  axios.delete(`/api/admin/reviews/files/trash/${encodeURIComponent(id)}`);

/** 清空审核回收站（仅管理员，内容只可永久删除）。 */
export const emptyReviewedTrash = () =>
  axios.delete('/api/admin/reviews/files/trash');

export const fetchAdminSystemConfig = () => axios.get('/api/admin/system-config');

export const updateAdminSystemConfig = (payload: Record<string, unknown>) =>
  axios.put('/api/admin/system-config', payload);

export const testAdminUpload = (
  sizeBytes: number,
  body: Blob,
  config?: AxiosRequestConfig
) =>
  axios.post(
    `/api/admin/system-config/upload-test?sizeBytes=${sizeBytes}`,
    body,
    config
  );

export const uploadSiteIcon = (payload: FormData) =>
  axios.post('/api/admin/site-icon', payload);

export const deleteSiteIcon = () => axios.delete('/api/admin/site-icon');

export const fetchCustomHomepage = () => axios.get('/api/admin/homepage');

export const saveCustomHomepage = (payload: Record<string, unknown>) =>
  axios.post('/api/admin/homepage', payload);

export const deleteCustomHomepage = () => axios.delete('/api/admin/homepage');
