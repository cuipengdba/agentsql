import {
  AuditOutlined,
  BellOutlined,
  DashboardOutlined,
  DatabaseOutlined,
  ExperimentOutlined,
  EyeInvisibleOutlined,
  FileSearchOutlined,
  FilterOutlined,
  RobotOutlined,
  SafetyOutlined,
} from "@ant-design/icons";
import type { ReactNode } from "react";

import { Agents } from "@/pages/Agents";
import { Approvals } from "@/pages/Approvals";
import { Audit } from "@/pages/Audit";
import { Datasources } from "@/pages/Datasources";
import { MaskRules } from "@/pages/MaskRules";
import { Notifications } from "@/pages/Notifications";
import { Overview } from "@/pages/Overview";
import { Playground } from "@/pages/Playground";
import { Policies } from "@/pages/Policies";
import { Rules } from "@/pages/Rules";

export interface MenuRoute {
  key: string;
  path: string;
  label: string;
  icon: ReactNode;
  element: ReactNode;
  group?: "settings";
}

export const menuRoutes: MenuRoute[] = [
  { key: "overview", path: "/", label: "总览", icon: <DashboardOutlined />, element: <Overview /> },
  { key: "audit", path: "/audit", label: "审计", icon: <FileSearchOutlined />, element: <Audit /> },
  {
    key: "playground",
    path: "/playground",
    label: "演示台",
    icon: <ExperimentOutlined />,
    element: <Playground />,
  },
  { key: "agents", path: "/agents", label: "Agent", icon: <RobotOutlined />, element: <Agents /> },
  {
    key: "datasources",
    path: "/datasources",
    label: "数据源",
    icon: <DatabaseOutlined />,
    element: <Datasources />,
  },
  { key: "policies", path: "/policies", label: "权限", icon: <SafetyOutlined />, element: <Policies /> },
  { key: "rules", path: "/rules", label: "规则", icon: <FilterOutlined />, element: <Rules /> },
  {
    key: "approvals",
    path: "/approvals",
    label: "审批",
    icon: <AuditOutlined />,
    element: <Approvals />,
  },
  {
    key: "mask-rules",
    path: "/mask-rules",
    label: "脱敏",
    icon: <EyeInvisibleOutlined />,
    element: <MaskRules />,
  },
  {
    key: "notifications",
    path: "/settings/notifications",
    label: "通知设置",
    icon: <BellOutlined />,
    element: <Notifications />,
    group: "settings",
  },
];
