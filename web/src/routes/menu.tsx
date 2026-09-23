import {
  AuditOutlined,
  BellOutlined,
  DashboardOutlined,
  DatabaseOutlined,
  ExperimentOutlined,
  EyeInvisibleOutlined,
  FileSearchOutlined,
  FilterOutlined,
  KeyOutlined,
  RobotOutlined,
  SafetyOutlined,
} from "@ant-design/icons";
import { lazy, Suspense, type ComponentType, type ReactNode } from "react";

const Overview = lazy(() => import("@/pages/Overview").then(({ Overview }) => ({ default: Overview })));
const Audit = lazy(() => import("@/pages/Audit").then(({ Audit }) => ({ default: Audit })));
const Playground = lazy(() => import("@/pages/Playground").then(({ Playground }) => ({ default: Playground })));
const Agents = lazy(() => import("@/pages/Agents").then(({ Agents }) => ({ default: Agents })));
const Datasources = lazy(() => import("@/pages/Datasources").then(({ Datasources }) => ({ default: Datasources })));
const Policies = lazy(() => import("@/pages/Policies").then(({ Policies }) => ({ default: Policies })));
const Rules = lazy(() => import("@/pages/Rules").then(({ Rules }) => ({ default: Rules })));
const Approvals = lazy(() => import("@/pages/Approvals").then(({ Approvals }) => ({ default: Approvals })));
const MaskRules = lazy(() => import("@/pages/MaskRules").then(({ MaskRules }) => ({ default: MaskRules })));
const Notifications = lazy(() => import("@/pages/Notifications").then(({ Notifications }) => ({ default: Notifications })));
const RedactionKeys = lazy(() => import("@/pages/RedactionKeys").then(({ RedactionKeys }) => ({ default: RedactionKeys })));

function RouteLoading() {
  return (
    <div
      role="status"
      aria-live="polite"
      aria-label="页面加载中"
      style={{
        display: "grid",
        minHeight: "calc(100vh - 64px)",
        placeItems: "center",
        color: "var(--color-text-secondary)",
        background: "var(--color-page)",
      }}
    >
      <span style={{ fontSize: 13, letterSpacing: "0.08em" }}>加载中…</span>
    </div>
  );
}

function lazyElement(Page: ComponentType): ReactNode {
  return (
    <Suspense fallback={<RouteLoading />}>
      <Page />
    </Suspense>
  );
}

export interface MenuRoute {
  key: string;
  path: string;
  label: string;
  icon: ReactNode;
  element: ReactNode;
  group?: "settings";
}

export const menuRoutes: MenuRoute[] = [
  { key: "overview", path: "/", label: "总览", icon: <DashboardOutlined />, element: lazyElement(Overview) },
  { key: "audit", path: "/audit", label: "审计", icon: <FileSearchOutlined />, element: lazyElement(Audit) },
  {
    key: "playground",
    path: "/playground",
    label: "演示台",
    icon: <ExperimentOutlined />,
    element: lazyElement(Playground),
  },
  { key: "agents", path: "/agents", label: "Agent", icon: <RobotOutlined />, element: lazyElement(Agents) },
  {
    key: "datasources",
    path: "/datasources",
    label: "数据源",
    icon: <DatabaseOutlined />,
    element: lazyElement(Datasources),
  },
  { key: "policies", path: "/policies", label: "权限", icon: <SafetyOutlined />, element: lazyElement(Policies) },
  { key: "rules", path: "/rules", label: "规则", icon: <FilterOutlined />, element: lazyElement(Rules) },
  {
    key: "approvals",
    path: "/approvals",
    label: "审批",
    icon: <AuditOutlined />,
    element: lazyElement(Approvals),
  },
  {
    key: "mask-rules",
    path: "/mask-rules",
    label: "脱敏",
    icon: <EyeInvisibleOutlined />,
    element: lazyElement(MaskRules),
  },
  {
    key: "redaction-keys",
    path: "/settings/redaction-keys",
    label: "脱敏密钥",
    icon: <KeyOutlined />,
    element: lazyElement(RedactionKeys),
    group: "settings",
  },
  {
    key: "notifications",
    path: "/settings/notifications",
    label: "通知设置",
    icon: <BellOutlined />,
    element: lazyElement(Notifications),
    group: "settings",
  },
];
