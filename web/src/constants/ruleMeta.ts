export interface RuleMeta {
  id: string;
  title: string;
  risk: number;
  dbType: "all" | "postgres" | "mysql";
  group: "通用防护" | "PostgreSQL 专属" | "MySQL 专属";
  dynamic: boolean;
  builtin: true;
  patternType: "ast";
}

export const ruleMeta: Record<string, RuleMeta> = {
  R001: { id: "R001", title: "多语句堆叠防护", risk: 5, dbType: "all", group: "通用防护", dynamic: false, builtin: true, patternType: "ast" },
  R002: { id: "R002", title: "无条件批量写防护", risk: 5, dbType: "all", group: "通用防护", dynamic: false, builtin: true, patternType: "ast" },
  R003: { id: "R003", title: "只读 Agent 写操作防护", risk: 5, dbType: "all", group: "通用防护", dynamic: false, builtin: true, patternType: "ast" },
  R004: { id: "R004", title: "大范围扫描审批", risk: 4, dbType: "all", group: "通用防护", dynamic: true, builtin: true, patternType: "ast" },
  R005: { id: "R005", title: "大结果集限制提醒", risk: 3, dbType: "all", group: "通用防护", dynamic: true, builtin: true, patternType: "ast" },
  R006: { id: "R006", title: "注释与未知语句注入防护", risk: 5, dbType: "all", group: "通用防护", dynamic: false, builtin: true, patternType: "ast" },
  R007: { id: "R007", title: "危险函数黑名单", risk: 5, dbType: "all", group: "通用防护", dynamic: false, builtin: true, patternType: "ast" },
  R008: { id: "R008", title: "请求速率与并发限制", risk: 5, dbType: "all", group: "通用防护", dynamic: false, builtin: true, patternType: "ast" },
  R009: { id: "R009", title: "SQL 复杂度限制", risk: 4, dbType: "all", group: "通用防护", dynamic: false, builtin: true, patternType: "ast" },
  R010: { id: "R010", title: "越权表访问防护", risk: 5, dbType: "all", group: "通用防护", dynamic: false, builtin: true, patternType: "ast" },
  R101: { id: "R101", title: "高危 DROP 防护", risk: 5, dbType: "postgres", group: "PostgreSQL 专属", dynamic: false, builtin: true, patternType: "ast" },
  R102: { id: "R102", title: "持重锁操作审批", risk: 4, dbType: "postgres", group: "PostgreSQL 专属", dynamic: false, builtin: true, patternType: "ast" },
  R103: { id: "R103", title: "管理与文件函数防护", risk: 5, dbType: "postgres", group: "PostgreSQL 专属", dynamic: false, builtin: true, patternType: "ast" },
  R104: { id: "R104", title: "COPY 外部程序防护", risk: 5, dbType: "postgres", group: "PostgreSQL 专属", dynamic: false, builtin: true, patternType: "ast" },
  R105: { id: "R105", title: "无索引批量写审批", risk: 4, dbType: "postgres", group: "PostgreSQL 专属", dynamic: true, builtin: true, patternType: "ast" },
  R106: { id: "R106", title: "大表结构变更审批", risk: 4, dbType: "postgres", group: "PostgreSQL 专属", dynamic: true, builtin: true, patternType: "ast" },
  R107: { id: "R107", title: "长事务与空闲事务告警", risk: 3, dbType: "postgres", group: "PostgreSQL 专属", dynamic: true, builtin: true, patternType: "ast" },
  R201: { id: "R201", title: "MySQL 文件读写防护", risk: 5, dbType: "mysql", group: "MySQL 专属", dynamic: false, builtin: true, patternType: "ast" },
  R202: { id: "R202", title: "MySQL 危险批量写审批", risk: 4, dbType: "mysql", group: "MySQL 专属", dynamic: false, builtin: true, patternType: "ast" },
  R203: { id: "R203", title: "MySQL 高危管理命令", risk: 5, dbType: "mysql", group: "MySQL 专属", dynamic: false, builtin: true, patternType: "ast" },
  R204: { id: "R204", title: "MySQL 大事务审批", risk: 4, dbType: "mysql", group: "MySQL 专属", dynamic: true, builtin: true, patternType: "ast" },
};

export function getRuleMeta(ruleID: string | null | undefined): RuleMeta {
  const id = (ruleID || "").trim();
  return ruleMeta[id] || {
    id: id || "未知规则",
    title: id || "未知规则",
    risk: 0,
    dbType: "all",
    group: "通用防护",
    dynamic: false,
    builtin: true,
    patternType: "ast",
  };
}
