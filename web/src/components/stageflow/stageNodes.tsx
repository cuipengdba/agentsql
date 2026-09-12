import {
  BranchesOutlined,
  CodeOutlined,
  FileDoneOutlined,
  FilterOutlined,
  SafetyCertificateOutlined,
  ThunderboltOutlined,
} from "@ant-design/icons";
import type { ReactNode } from "react";

import type { StageKey } from "./types";

export interface StageNodeMeta {
  key: StageKey;
  label: string;
  subtitle: string;
  icon: ReactNode;
  latencyKeys: readonly string[];
}

export const stageNodes: readonly StageNodeMeta[] = [
  {
    key: "auth",
    label: "鉴权",
    subtitle: "身份与资源加载",
    icon: <SafetyCertificateOutlined />,
    latencyKeys: ["auth", "load"],
  },
  {
    key: "parse",
    label: "解析",
    subtitle: "SQL 转结构化 AST",
    icon: <CodeOutlined />,
    latencyKeys: ["parse"],
  },
  {
    key: "guard",
    label: "安检",
    subtitle: "静态与动态双闸",
    icon: <FilterOutlined />,
    latencyKeys: ["guard_static", "guard_dynamic"],
  },
  {
    key: "decide",
    label: "决策",
    subtitle: "四态优先级聚合",
    icon: <BranchesOutlined />,
    latencyKeys: [],
  },
  {
    key: "execute",
    label: "执行",
    subtitle: "受控连接与限量",
    icon: <ThunderboltOutlined />,
    latencyKeys: ["execute"],
  },
  {
    key: "audit",
    label: "留痕",
    subtitle: "脱敏与同步审计",
    icon: <FileDoneOutlined />,
    latencyKeys: ["redact", "audit"],
  },
] as const;

export const stageOrder: readonly StageKey[] = stageNodes.map((node) => node.key);
