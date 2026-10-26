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
  SafetyCertificateOutlined,
  SafetyOutlined,
  SwapOutlined,
  CloudDownloadOutlined,
  TeamOutlined,
  ApartmentOutlined,
  IdcardOutlined,
  RocketOutlined,
  ClockCircleOutlined,
} from "@ant-design/icons";
import { lazy, Suspense, type ComponentType, type ReactNode } from "react";

const Overview = lazy(() => import("@/pages/Overview").then(({ Overview }) => ({ default: Overview })));
const Audit = lazy(() => import("@/pages/Audit").then(({ Audit }) => ({ default: Audit })));
const Playground = lazy(() => import("@/pages/Playground").then(({ Playground }) => ({ default: Playground })));
const Agents = lazy(() => import("@/pages/Agents").then(({ Agents }) => ({ default: Agents })));
const Datasources = lazy(() => import("@/pages/Datasources").then(({ Datasources }) => ({ default: Datasources })));
const Policies = lazy(() => import("@/pages/Policies").then(({ Policies }) => ({ default: Policies })));
const PolicyBundles = lazy(() => import("@/pages/PolicyBundles").then(({ PolicyBundles }) => ({ default: PolicyBundles })));
const Rules = lazy(() => import("@/pages/Rules").then(({ Rules }) => ({ default: Rules })));
const Approvals = lazy(() => import("@/pages/Approvals").then(({ Approvals }) => ({ default: Approvals })));
const MaskRules = lazy(() => import("@/pages/MaskRules").then(({ MaskRules }) => ({ default: MaskRules })));
const Notifications = lazy(() => import("@/pages/Notifications").then(({ Notifications }) => ({ default: Notifications })));
const SQLBot = lazy(() => import("@/pages/SQLBot").then(({ SQLBot }) => ({ default: SQLBot })));
const RedactionKeys = lazy(() => import("@/pages/RedactionKeys").then(({ RedactionKeys }) => ({ default: RedactionKeys })));
const AuditChain = lazy(() => import("@/pages/AuditChain").then(({ AuditChain }) => ({ default: AuditChain })));
const B5Operations = lazy(() => import("@/pages/B5Operations").then(({ B5Operations }) => ({ default: B5Operations })));
const Security = lazy(() => import("@/pages/Security").then(({ Security }) => ({ default: Security })));
const Upgrade = lazy(() => import("@/pages/Upgrade").then(({ Upgrade }) => ({ default: Upgrade })));
const Users = lazy(() => import("@/pages/Users").then(({ Users }) => ({ default: Users })));
const Roles = lazy(() => import("@/pages/Roles").then(({ Roles }) => ({ default: Roles })));
const Tenants = lazy(() => import("@/pages/Tenants").then(({ Tenants }) => ({ default: Tenants })));
const Onboarding = lazy(() => import("@/pages/Onboarding").then(({ Onboarding }) => ({ default: Onboarding })));
const TaskReadGrants = lazy(() => import("@/pages/TaskReadGrants").then(({ TaskReadGrants }) => ({ default: TaskReadGrants })));

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
  labelEn: string;
  icon: ReactNode;
  element: ReactNode;
  group?: "settings";
  feature?: "b5";
  permission?: string;
  permissions?: string[];
}

export const menuRoutes: MenuRoute[] = [
  { key: "sqlbot", path: "/settings/sqlbot", label: "问数与 SQLBot", labelEn: "Q&A and SQLBot", icon: <RobotOutlined />, element: lazyElement(SQLBot), group: "settings", permissions: ["strategy.manage", "user.manage", "datasource.manage"] },
  { key: "upgrade", path: "/settings/upgrade", label: "版本与升级", labelEn: "Version & upgrade", icon: <CloudDownloadOutlined />, element: lazyElement(Upgrade), group: "settings" },
  { key: "overview", path: "/", label: "总览", labelEn: "Overview", icon: <DashboardOutlined />, element: lazyElement(Overview) },
  { key: "onboarding", path: "/onboarding", label: "接入向导", labelEn: "Setup guide", icon: <RocketOutlined />, element: lazyElement(Onboarding), permission: "datasource.manage" },
  { key: "audit", path: "/audit", label: "审计", labelEn: "Audit", icon: <FileSearchOutlined />, element: lazyElement(Audit) },
  {
    key: "playground",
    path: "/playground",
    label: "演示台",
    labelEn: "Playground",
    icon: <ExperimentOutlined />,
    element: lazyElement(Playground),
  },
  { key: "agents", path: "/agents", label: "Agent", labelEn: "Agents", icon: <RobotOutlined />, element: lazyElement(Agents) },
  {
    key: "datasources",
    path: "/datasources",
    label: "数据源",
    labelEn: "Data sources",
    icon: <DatabaseOutlined />,
    element: lazyElement(Datasources),
  },
  { key: "policies", path: "/policies", label: "权限", labelEn: "Permissions", icon: <SafetyOutlined />, element: lazyElement(Policies) },
  { key: "policy-bundles", path: "/policy-bundles", label: "策略包治理", labelEn: "Policy bundles", icon: <SafetyCertificateOutlined />, element: lazyElement(PolicyBundles), permission: "strategy.manage" },
  { key: "task-read-grants", path: "/task-read-grants", label: "任务授权", labelEn: "Task grants", icon: <ClockCircleOutlined />, element: lazyElement(TaskReadGrants), permission: "query.execute" },
  { key: "users", path: "/users", label: "用户", labelEn: "Users", icon: <TeamOutlined />, element: lazyElement(Users), group: "settings", permission: "user.manage" },
  { key: "roles", path: "/roles", label: "角色", labelEn: "Roles", icon: <IdcardOutlined />, element: lazyElement(Roles), group: "settings", permission: "role.manage" },
  { key: "tenants", path: "/tenants", label: "租户", labelEn: "Tenants", icon: <ApartmentOutlined />, element: lazyElement(Tenants), group: "settings", permission: "tenant.manage" },
  { key: "rules", path: "/rules", label: "规则", labelEn: "Rules", icon: <FilterOutlined />, element: lazyElement(Rules) },
  {
    key: "approvals",
    path: "/approvals",
    label: "审批",
    labelEn: "Approvals",
    icon: <AuditOutlined />,
    element: lazyElement(Approvals),
  },
  {
    key: "b5-operations",
    path: "/b5-operations",
    label: "会话/事务",
    labelEn: "Sessions & transactions",
    icon: <SwapOutlined />,
    element: lazyElement(B5Operations),
    feature: "b5",
  },
  {
    key: "mask-rules",
    path: "/mask-rules",
    label: "脱敏",
    labelEn: "Masking",
    icon: <EyeInvisibleOutlined />,
    element: lazyElement(MaskRules),
  },
  {
    key: "redaction-keys",
    path: "/settings/redaction-keys",
    label: "脱敏密钥",
    labelEn: "Masking keys",
    icon: <KeyOutlined />,
    element: lazyElement(RedactionKeys),
    group: "settings",
  },
  {
    key: "audit-chain",
    path: "/settings/audit-chain",
    label: "审计完整性链",
    labelEn: "Audit integrity chain",
    icon: <SafetyCertificateOutlined />,
    element: lazyElement(AuditChain),
    group: "settings",
  },
  {
    key: "security",
    path: "/settings/security",
    label: "登录安全",
    labelEn: "Sign-in security",
    icon: <SafetyCertificateOutlined />,
    element: lazyElement(Security),
    group: "settings",
  },
  {
    key: "notifications",
    path: "/settings/notifications",
    label: "通知设置",
    labelEn: "Notifications",
    icon: <BellOutlined />,
    element: lazyElement(Notifications),
    group: "settings",
  },
];

export function menuRoutesForFeatures(b5Enabled: boolean, permissions: string[] = []): MenuRoute[] {
  return menuRoutes.filter((route) =>
    (route.feature !== "b5" || b5Enabled) &&
    (!route.permission || permissions.includes(route.permission)) &&
    (!route.permissions || route.permissions.every((permission) => permissions.includes(permission))),
  );
}
