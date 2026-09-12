import { BulbOutlined, ExclamationCircleFilled } from "@ant-design/icons";
import { Tag } from "antd";
import type { CSSProperties } from "react";

import { decisionMeta } from "@/constants/labels";
import { getRuleMeta } from "@/constants/ruleMeta";
import { palette } from "@/theme/tokens";
import { useThemeStore } from "@/theme/useThemeStore";

import type { FlowDecision, StageVerdictData } from "./types";

interface StageVerdictProps {
  data: StageVerdictData;
  decision?: FlowDecision;
  dense?: boolean;
}

interface VerdictStyle extends CSSProperties {
  "--verdict-color": string;
  "--verdict-surface": string;
}

function verdictColor(decision: FlowDecision): string {
  if (decision === "approve") return decisionMeta.approve.color;
  if (decision === "warn") return decisionMeta.warn.color;
  if (decision === "allow") return decisionMeta.allow.color;
  return decisionMeta.deny.color;
}

export function StageVerdict({ data, decision = "deny", dense = false }: StageVerdictProps) {
  const mode = useThemeStore((state) => state.mode);
  const colors = palette[mode];
  const meta = getRuleMeta(data.ruleId);
  const title = data.title?.trim() || meta.title;
  const ruleID = data.ruleId?.trim() || "安全判定";
  const style: VerdictStyle = {
    "--verdict-color": verdictColor(decision),
    "--verdict-surface": colors.elevated,
  };

  return (
    <section className={`stage-verdict${dense ? " stage-verdict-dense" : ""}`} style={style} aria-label="安全判词">
      <div className="stage-verdict-bar" />
      <div className="stage-verdict-body">
        <div className="stage-verdict-heading">
          <span className="stage-verdict-icon" aria-hidden="true"><ExclamationCircleFilled /></span>
          <Tag color={decision === "approve" ? "orange" : decision === "warn" ? "warning" : "error"}>{ruleID}</Tag>
          <strong>{title}</strong>
          {data.risk !== undefined && Number.isFinite(data.risk) ? <span className="stage-verdict-risk">风险 {data.risk}</span> : null}
        </div>
        <p className="stage-verdict-message">{data.message?.trim() || "安全策略未给出详细判词"}</p>
        {data.suggestion?.trim() ? (
          <div className="stage-verdict-suggestion">
            <BulbOutlined aria-hidden="true" />
            <span><strong>改写建议：</strong>{data.suggestion}</span>
          </div>
        ) : null}
      </div>
    </section>
  );
}
