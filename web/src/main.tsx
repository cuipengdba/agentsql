import { ConfigProvider } from "antd";
import enUS from "antd/locale/en_US";
import zhCN from "antd/locale/zh_CN";
import { StrictMode, useEffect, useLayoutEffect } from "react";
import { createRoot } from "react-dom/client";

import { App } from "@/App";
import { useLocaleStore } from "@/i18n/locale";
import { installUiTranslation } from "@/i18n/translateUi";
import "@/styles.css";
import { useDemoStore } from "@/store/demoStore";
import { themeConfig } from "@/theme/tokens";
import { useThemeStore } from "@/theme/useThemeStore";

function Root() {
  const mode = useThemeStore((state) => state.mode);
  const locale = useLocaleStore((state) => state.locale);

  useEffect(() => {
    document.documentElement.dataset.theme = mode;
  }, [mode]);

  useEffect(() => {
    document.documentElement.lang = locale;
    document.title = locale === "en-US" ? "AgentSQL Control Console" : "AgentSQL 智盾控制台";
  }, [locale]);

  useLayoutEffect(() => installUiTranslation(locale), [locale]);

  useEffect(() => {
    void useDemoStore.getState().load();
  }, []);

  return (
    <ConfigProvider theme={themeConfig(mode)} locale={locale === "en-US" ? enUS : zhCN}>
      <App />
    </ConfigProvider>
  );
}

const rootElement = document.getElementById("root");
if (!rootElement) {
  throw new Error("missing root element");
}

createRoot(rootElement).render(
  <StrictMode>
    <Root />
  </StrictMode>,
);
