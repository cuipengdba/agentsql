import { ConfigProvider } from "antd";
import { StrictMode, useEffect } from "react";
import { createRoot } from "react-dom/client";

import { App } from "@/App";
import "@/styles.css";
import { useDemoStore } from "@/store/demoStore";
import { themeConfig } from "@/theme/tokens";
import { useThemeStore } from "@/theme/useThemeStore";

function Root() {
  const mode = useThemeStore((state) => state.mode);

  useEffect(() => {
    document.documentElement.dataset.theme = mode;
  }, [mode]);

  useEffect(() => {
    void useDemoStore.getState().load();
  }, []);

  return (
    <ConfigProvider theme={themeConfig(mode)}>
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
