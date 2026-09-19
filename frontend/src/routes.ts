export type IRoute = {
  name: string;
  key: string;
  adminPermissions?: string[];
  // permissions 是用户组权限（userInfo.permissions，如 direct_link）：
  // 未命中其中任何一项时不显示该入口，也不注册对应路由。
  permissions?: string[];
};

export const hasManagementAccess = (userInfo) =>
  Boolean(userInfo?.isSuperAdmin || userInfo?.adminPermissions?.length);

export const routes: IRoute[] = [
  { name: 'menu.files', key: 'files' },
  {
    name: 'menu.shares',
    key: 'shares',
    permissions: ['share', 'pickup_share'],
  },
  {
    name: 'menu.directLinks',
    key: 'direct-links',
    permissions: ['direct_link'],
  },
  { name: 'menu.trash', key: 'trash' },
];

export const adminRoutes: IRoute[] = [
  {
    name: 'menu.admin.overview',
    key: 'admin/overview',
    adminPermissions: ['view_admin_overview'],
  },
  {
    name: 'menu.admin.users',
    key: 'admin/users',
    adminPermissions: ['manage_users', 'manage_user_groups'],
  },
  {
    name: 'menu.admin.access',
    key: 'admin/access',
    adminPermissions: [
      'manage_permissions',
      'manage_quotas',
      'manage_invitations',
      // 用户组存储绑定属于系统管理权限：仅持 manage_system 的管理员也需要入口，
      // 与后端 access 门禁的放行条件保持一致。
      'manage_system',
    ],
  },
  {
    name: 'menu.admin.audit',
    key: 'admin/audit',
    adminPermissions: ['review_files', 'review_shares', 'read_audit_log'],
  },
  {
    name: 'menu.admin.storage',
    key: 'admin/storage',
    adminPermissions: ['manage_system'],
  },
  {
    name: 'menu.admin.system',
    key: 'admin/system',
    adminPermissions: ['manage_system'],
  },
];

export const getRoutesForUser = (
  userInfo,
  adminMode = false
): [IRoute[], string] => {
  const adminPermissions = userInfo?.adminPermissions || [];
  const permissions = userInfo?.permissions || [];
  const sourceRoutes =
    adminMode && hasManagementAccess(userInfo) ? adminRoutes : routes;
  const visibleRoutes = sourceRoutes.filter((route) => {
    if (
      route.adminPermissions?.length &&
      !userInfo?.isSuperAdmin &&
      !route.adminPermissions.some((permission) =>
        adminPermissions.includes(permission)
      )
    ) {
      return false;
    }
    // 用户组权限：超管的 permissions 是全量授权，因此无需额外分支。
    if (
      route.permissions?.length &&
      !route.permissions.some((permission) => permissions.includes(permission))
    ) {
      return false;
    }
    return true;
  });

  return [visibleRoutes, visibleRoutes[0]?.key || ''];
};
