export interface RuleMeta {
  id: string;
  title: string;
  risk: number;
}

export const ruleMeta: Record<string, RuleMeta> = {
  R001: { id: "R001", title: "多语句堆叠防护", risk: 5 },
  R002: { id: "R002", title: "无条件批量写防护", risk: 5 },
  R003: { id: "R003", title: "只读 Agent 写操作防护", risk: 5 },
  R004: { id: "R004", title: "大范围扫描审批", risk: 4 },
  R005: { id: "R005", title: "大结果集限制提醒", risk: 3 },
  R006: { id: "R006", title: "注释与未知语句注入防护", risk: 5 },
  R007: { id: "R007", title: "危险函数黑名单", risk: 5 },
  R008: { id: "R008", title: "请求速率与并发限制", risk: 5 },
  R009: { id: "R009", title: "SQL 复杂度限制", risk: 4 },
  R010: { id: "R010", title: "越权表访问防护", risk: 5 },
  R101: { id: "R101", title: "高危 DROP 防护", risk: 5 },
  R102: { id: "R102", title: "持重锁操作审批", risk: 4 },
  R103: { id: "R103", title: "管理与文件函数防护", risk: 5 },
  R104: { id: "R104", title: "COPY 外部程序防护", risk: 5 },
  R105: { id: "R105", title: "无索引批量写审批", risk: 4 },
  R106: { id: "R106", title: "大表结构变更审批", risk: 4 },
  R107: { id: "R107", title: "长事务与空闲事务告警", risk: 3 },
  R201: { id: "R201", title: "MySQL 文件读写防护", risk: 5 },
  R202: { id: "R202", title: "MySQL 危险批量写审批", risk: 4 },
  R203: { id: "R203", title: "MySQL 高危管理命令", risk: 5 },
  R204: { id: "R204", title: "MySQL 大事务审批", risk: 4 },
};

export function getRuleMeta(ruleID: string | null | undefined): RuleMeta {
  const id = (ruleID || "").trim();
  return ruleMeta[id] || { id: id || "未知规则", title: id || "未知规则", risk: 0 };
}
