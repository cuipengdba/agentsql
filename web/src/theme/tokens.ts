import { theme, type ThemeConfig } from "antd";

export type ThemeMode = "dark" | "light";

export const palette = {
  dark: {
    page: "#0d1421",
    surface: "#142033",
    elevated: "#1c2b42",
    border: "#2a3b55",
    brand: "#3b9eff",
    text: "#f1f5fb",
    textSecondary: "#9dafc6",
  },
  light: {
    page: "#f5f7fa",
    surface: "#ffffff",
    elevated: "#ffffff",
    border: "#d9e0e8",
    brand: "#1677ff",
    text: "#172033",
    textSecondary: "#66758a",
  },
  semantic: {
    allow: "#52c41a",
    warn: "#faad14",
    approve: "#fa8c16",
    deny: "#f5222d",
  },
} as const;

export const fontFamily = '"Noto Sans SC", "Microsoft YaHei", system-ui, sans-serif';
export const monoFontFamily = '"JetBrains Mono", Consolas, monospace';

export function themeConfig(mode: ThemeMode): ThemeConfig {
  const colors = palette[mode];
  return {
    algorithm: mode === "dark" ? theme.darkAlgorithm : theme.defaultAlgorithm,
    token: {
      colorPrimary: colors.brand,
      colorBgBase: colors.page,
      colorBgContainer: colors.surface,
      colorBorder: colors.border,
      colorText: colors.text,
      colorTextSecondary: colors.textSecondary,
      colorSuccess: palette.semantic.allow,
      colorWarning: palette.semantic.warn,
      colorError: palette.semantic.deny,
      borderRadius: 6,
      fontFamily,
    },
    components: {
      Layout: {
        bodyBg: colors.page,
        headerBg: colors.surface,
        siderBg: palette.dark.page,
      },
      Menu: {
        darkItemBg: palette.dark.page,
        darkSubMenuItemBg: palette.dark.page,
        darkItemSelectedBg: palette.dark.elevated,
      },
      Card: {
        colorBorderSecondary: colors.border,
      },
    },
  };
}
