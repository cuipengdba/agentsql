import {
  LogoutOutlined,
  MenuFoldOutlined,
  MenuUnfoldOutlined,
  MoonOutlined,
  SafetyCertificateFilled,
  SunOutlined,
  UserOutlined,
} from "@ant-design/icons";
import { Alert, Avatar, Button, Layout, Menu, Space, Tag, Tooltip, Typography } from "antd";
import type { MenuProps } from "antd";
import { useState } from "react";
import { Outlet, useLocation, useNavigate } from "react-router-dom";

import { logout as logoutRequest } from "@/api/auth";
import { menuRoutes } from "@/routes/menu";
import { useAuthStore } from "@/store/authStore";
import { selectIsDemo, useDemoStore } from "@/store/demoStore";
import { useThemeStore } from "@/theme/useThemeStore";

const { Header, Sider, Content } = Layout;

export function MainLayout() {
  const [collapsed, setCollapsed] = useState(false);
  const [bannerVisible, setBannerVisible] = useState(true);
  const location = useLocation();
  const navigate = useNavigate();
  const username = useAuthStore((state) => state.username);
  const clear = useAuthStore((state) => state.clear);
  const themeMode = useThemeStore((state) => state.mode);
  const toggleTheme = useThemeStore((state) => state.toggle);
  const demoEnabled = useDemoStore(selectIsDemo);
  const demoBanner = useDemoStore((state) => state.banner);
  const currentRoute = menuRoutes.find((route) => route.path === location.pathname);
  const menuItem = ({ key, label, icon }: (typeof menuRoutes)[number]): NonNullable<MenuProps["items"]>[number] => ({
    key,
    icon,
    label: key === "playground" && demoEnabled
      ? <span className="demo-menu-label"><span>{label}</span><Tag color="processing">Live Demo</Tag></span>
      : label,
  });
  const primaryRoutes = menuRoutes.filter((route) => route.group !== "settings");
  const settingsRoutes = menuRoutes.filter((route) => route.group === "settings");
  const menuItems: MenuProps["items"] = [
    ...primaryRoutes.map(menuItem),
    ...(settingsRoutes.length > 0 ? [{
      type: "group" as const,
      label: "设置与集成",
      children: settingsRoutes.map(menuItem),
    }] : []),
  ];

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
      <Sider className="app-sider" theme="dark" width={224} collapsedWidth={72} collapsed={collapsed}>
        <button className="brand" type="button" onClick={() => navigate("/")} aria-label="返回总览">
          <SafetyCertificateFilled className="brand-icon" />
          {!collapsed ? <span>AgentSQL 智盾</span> : null}
        </button>
        <Menu
          theme="dark"
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
      </Sider>
      <Layout>
        <Header className="app-header">
          <Space size={12}>
            <Tooltip title={collapsed ? "展开导航" : "收起导航"}>
              <Button
                type="text"
                className="header-icon-button"
                icon={collapsed ? <MenuUnfoldOutlined /> : <MenuFoldOutlined />}
                onClick={() => setCollapsed((value) => !value)}
                aria-label={collapsed ? "展开导航" : "收起导航"}
              />
            </Tooltip>
            <Typography.Text className="current-page-title">{currentRoute?.label || "AgentSQL"}</Typography.Text>
            {demoEnabled ? <Tag color="processing" className="live-demo-tag">Live Demo</Tag> : null}
          </Space>
          <Space size={12}>
            <Tooltip title={themeMode === "dark" ? "切换浅色主题" : "切换深色主题"}>
              <Button
                type="text"
                className="header-icon-button"
                icon={themeMode === "dark" ? <SunOutlined /> : <MoonOutlined />}
                onClick={toggleTheme}
                aria-label="切换主题"
              />
            </Tooltip>
            <span className="admin-identity">
              <Avatar size={28} icon={<UserOutlined />} />
              <span>{username || "admin"}</span>
            </span>
            <Tooltip title="退出登录">
              <Button
                type="text"
                danger
                className="header-icon-button"
                icon={<LogoutOutlined />}
                onClick={() => void handleLogout()}
                aria-label="退出登录"
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
