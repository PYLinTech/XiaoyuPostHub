export type IRoute = {
  name: string;
  key: string;
  adminPermissions?: string[];
};

export const hasManagementAccess = (userInfo) =>
  Boolean(userInfo?.isSuperAdmin || userInfo?.adminPermissions?.length);

export const routes: IRoute[] = [
  { name: 'menu.files', key: 'files' },
  { name: 'menu.shares', key: 'shares' },
  { name: 'menu.directLinks', key: 'direct-links' },
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
    ],
  },
  {
    name: 'menu.admin.audit',
    key: 'admin/audit',
    adminPermissions: ['review_files', 'review_shares', 'read_audit_log'],
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
  const sourceRoutes =
    adminMode && hasManagementAccess(userInfo) ? adminRoutes : routes;
  const visibleRoutes = sourceRoutes.filter((route) => {
    return !(
      route.adminPermissions?.length &&
      !userInfo?.isSuperAdmin &&
      !route.adminPermissions.some((permission) =>
        adminPermissions.includes(permission)
      )
    );
  });

  return [visibleRoutes, visibleRoutes[0]?.key || ''];
};
