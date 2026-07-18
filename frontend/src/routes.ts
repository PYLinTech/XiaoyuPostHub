export type IRoute = {
  name: string;
  key: string;
  adminPermission?: string;
  superAdminOnly?: boolean;
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
  { name: 'menu.admin.overview', key: 'admin/overview' },
  {
    name: 'menu.admin.users',
    key: 'admin/users',
    adminPermission: 'manage_users',
  },
  {
    name: 'menu.admin.access',
    key: 'admin/access',
    adminPermission: 'manage_roles',
  },
  {
    name: 'menu.admin.audit',
    key: 'admin/audit',
    adminPermission: 'read_audit_log',
  },
  { name: 'menu.admin.system', key: 'admin/system', superAdminOnly: true },
];

export const getRoutesForUser = (
  userInfo,
  adminMode = false
): [IRoute[], string] => {
  const adminPermissions = userInfo?.adminPermissions || [];
  const sourceRoutes =
    adminMode && hasManagementAccess(userInfo) ? adminRoutes : routes;
  const visibleRoutes = sourceRoutes.filter((route) => {
    if (route.superAdminOnly && !userInfo?.isSuperAdmin) return false;
    return !(
      route.adminPermission &&
      !userInfo?.isSuperAdmin &&
      !adminPermissions.includes(route.adminPermission)
    );
  });

  return [visibleRoutes, visibleRoutes[0]?.key || ''];
};
