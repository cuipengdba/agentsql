import type { ReactNode } from "react";

interface PageContainerProps {
  title: string;
  subtitle?: string;
  extra?: ReactNode;
  children: ReactNode;
}

export function PageContainer({ title, subtitle, extra, children }: PageContainerProps) {
  return (
    <main className="page-container">
      <header className="page-heading">
        <div>
          <h1>{title}</h1>
          {subtitle ? <p>{subtitle}</p> : null}
        </div>
        {extra ? <div className="page-heading-extra">{extra}</div> : null}
      </header>
      <section className="page-content">{children}</section>
    </main>
  );
}
