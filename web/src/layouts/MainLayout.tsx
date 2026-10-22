import {
  LogoutOutlined,
  MenuFoldOutlined,
  MenuUnfoldOutlined,
  MoonOutlined,
  SunOutlined,
  UserOutlined,
} from "@ant-design/icons";
import { Alert, Avatar, Button, Layout, Menu, Space, Tag, Tooltip, Typography } from "antd";
import type { MenuProps } from "antd";
import { useEffect, useState } from "react";
import { Outlet, useLocation, useNavigate } from "react-router-dom";

import { logout as logoutRequest, me } from "@/api/auth";
import { getB5Status } from "@/api/b5";
import { localeText, useLocaleStore } from "@/i18n/locale";
import { menuRoutes, menuRoutesForFeatures } from "@/routes/menu";
import { useAuthStore } from "@/store/authStore";
import { selectIsDemo, useDemoStore } from "@/store/demoStore";
import brandMark from "@/assets/agentsql-mark.svg";
import { useThemeStore } from "@/theme/useThemeStore";

const { Header, Sider, Content } = Layout;

export function MainLayout() {
  const [collapsed, setCollapsed] = useState(false);
  const [bannerVisible, setBannerVisible] = useState(true);
  const [b5Enabled, setB5Enabled] = useState(false);
  const [permissions, setPermissions] = useState<string[]>([]);
  const [bootstrapAdmin, setBootstrapAdmin] = useState(false);
  const location = useLocation();
  const navigate = useNavigate();
  const username = useAuthStore((state) => state.username);
  const clear = useAuthStore((state) => state.clear);
  const themeMode = useThemeStore((state) => state.mode);
  const toggleTheme = useThemeStore((state) => state.toggle);
  const demoEnabled = useDemoStore(selectIsDemo);
  const demoBanner = useDemoStore((state) => state.banner);
  const locale = useLocaleStore((state) => state.locale);
  const setLocale = useLocaleStore((state) => state.setLocale);
  const tr = (zh: string, en: string) => localeText(zh, en, locale);
  const currentRoute = menuRoutes.find((route) => route.path === location.pathname);
  const menuItem = ({ key, label, labelEn, icon }: (typeof menuRoutes)[number]): NonNullable<MenuProps["items"]>[number] => ({
    key,
    icon,
    // Route metadata stays stable; only the displayed label changes.
    label: key === "playground" && demoEnabled
      ? <span className="demo-menu-label"><span>{locale === "en-US" ? labelEn : label}</span><Tag color="processing">Live Demo</Tag></span>
      : locale === "en-US" ? labelEn : label,
  });
  const visibleRoutes = menuRoutesForFeatures(b5Enabled, permissions).filter((route) => route.key !== "tenants" || bootstrapAdmin);
  const primaryRoutes = visibleRoutes.filter((route) => route.group !== "settings");
  const settingsRoutes = visibleRoutes.filter((route) => route.group === "settings");
  const menuItems: MenuProps["items"] = [
    ...primaryRoutes.map(menuItem),
    ...(settingsRoutes.length > 0 ? [{
      type: "group" as const,
      label: tr("设置与集成", "Settings & integrations"),
      children: settingsRoutes.map(menuItem),
    }] : []),
  ];

  useEffect(() => {
    const controller = new AbortController();
    void getB5Status(controller.signal)
      .then((status) => setB5Enabled(status.enabled))
      .catch(() => setB5Enabled(false));
    return () => controller.abort();
  }, []);

  useEffect(() => {
    let active = true;
    void me().then((principal) => {
      if (!active) return;
      setPermissions(principal.permissions || []);
      setBootstrapAdmin(principal.id === "user_bootstrap_admin" && principal.tenant_id === "tenant_default" && principal.permissions.includes("tenant.manage"));
    }).catch(() => { if (active) { setPermissions([]); setBootstrapAdmin(false); } });
    return () => { active = false; };
  }, []);

  const handleLogout = async () => {
    try {
      await logoutRequest();
    } finally {
      clear();
      navigate("/login", { replace: true });
    }
  };

  return (
    <Layout className="app-shell">
      <Sider className="app-sider" theme={themeMode} width={224} collapsedWidth={72} collapsed={collapsed}>
        <button className="brand" type="button" onClick={() => navigate("/")} aria-label={tr("返回总览", "Go to overview")}>
          <img className="brand-icon" src={brandMark} alt="" aria-hidden="true" width={32} height={32} />
          {!collapsed ? <span>{tr("AgentSQL 智盾", "AgentSQL")}</span> : null}
        </button>
        <nav className="app-sider-scroll" aria-label={tr("主导航", "Main navigation")} tabIndex={0}>
          <Menu
          theme={themeMode}
          mode="inline"
          selectedKeys={[currentRoute?.key || ""]}
          items={menuItems}
          onClick={({ key }) => {
            const target = menuRoutes.find((route) => route.key === key);
            if (target) {
              navigate(target.path);
            }
          }}
          />
        </nav>
      </Sider>
      <Layout>
        <Header className="app-header">
          <Space size={12}>
            <Tooltip title={collapsed ? tr("展开导航", "Expand navigation") : tr("收起导航", "Collapse navigation")}>
              <Button
                type="text"
                className="header-icon-button"
                icon={collapsed ? <MenuUnfoldOutlined /> : <MenuFoldOutlined />}
                onClick={() => setCollapsed((value) => !value)}
                aria-label={collapsed ? tr("展开导航", "Expand navigation") : tr("收起导航", "Collapse navigation")}
              />
            </Tooltip>
            <Typography.Text className="current-page-title">{currentRoute ? (locale === "en-US" ? currentRoute.labelEn : currentRoute.label) : "AgentSQL"}</Typography.Text>
            {demoEnabled ? <Tag color="processing" className="live-demo-tag">Live Demo</Tag> : null}
          </Space>
          <Space size={12}>
            <Button
              type="text"
              className="locale-button"
              data-no-translate
              onClick={() => setLocale(locale === "zh-CN" ? "en-US" : "zh-CN")}
              aria-label={tr("切换为英文", "Switch to Chinese")}
              title={tr("切换为英文", "Switch to Chinese")}
            >
              {locale === "zh-CN" ? "EN" : "中文"}
            </Button>
            <Tooltip title={themeMode === "dark" ? tr("切换浅色主题", "Switch to light theme") : tr("切换深色主题", "Switch to dark theme")}>
              <Button
                type="text"
                className="header-icon-button"
                icon={themeMode === "dark" ? <SunOutlined /> : <MoonOutlined />}
                onClick={toggleTheme}
                aria-label={tr("切换主题", "Toggle theme")}
              />
            </Tooltip>
            <span className="admin-identity">
              <Avatar size={28} icon={<UserOutlined />} />
              <span>{username || "admin"}</span>
            </span>
            <Tooltip title={tr("退出登录", "Sign out")}>
              <Button
                type="text"
                danger
                className="header-icon-button"
                icon={<LogoutOutlined />}
                onClick={() => void handleLogout()}
                aria-label={tr("退出登录", "Sign out")}
              />
            </Tooltip>
          </Space>
        </Header>
        {demoEnabled && bannerVisible ? (
          <Alert
            className="live-demo-banner"
            banner
            showIcon
            closable
            type="warning"
            message={demoBanner}
            onClose={() => setBannerVisible(false)}
          />
        ) : null}
        <Content className="app-content">
          <Outlet />
        </Content>
      </Layout>
    </Layout>
  );
}
