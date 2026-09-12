export type StageKey = "auth" | "parse" | "guard" | "decide" | "execute" | "audit";

export type StageStatus = "pending" | "active" | "pass" | "warn" | "block" | "skip" | "locked";

export type FlowDecision = "allow" | "warn" | "approve" | "deny" | "error";

export interface StageStep {
  key: StageKey;
  label: string;
  status: StageStatus;
  latencyMs?: number;
  note?: string;
}

export interface StageVerdictData {
  ruleId?: string;
  title?: string;
  message: string;
  suggestion?: string;
  risk?: number;
}

export interface StageFlowData {
  decision: FlowDecision;
  steps: StageStep[];
  blockAt?: StageKey;
  verdict?: StageVerdictData;
  rowsReturned?: number;
  totalLatencyMs?: number;
  errorMsg?: string;
}

export interface StageFlowProps {
  data: StageFlowData;
  autoPlay?: boolean;
  replayKey?: number | string;
  dense?: boolean;
  onStepChange?: (key: StageKey, index: number) => void;
  onFinish?: (decision: FlowDecision) => void;
}
