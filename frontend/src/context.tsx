import { createContext } from 'react';

export interface UserInfo {
  id?: number;
  name?: string;
  avatar?: string;
  permissions?: string[];
  adminPermissions?: string[];
  isSuperAdmin?: boolean;
}

export const GlobalContext = createContext<{
  lang?: string;
  setLang?: (value: string) => void;
  siteName?: string;
  siteIconUrl?: string;
  setSiteConfig?: (value: { siteName?: string; siteIconUrl?: string }) => void;
  userInfo?: UserInfo;
  userLoading?: boolean;
}>({});
