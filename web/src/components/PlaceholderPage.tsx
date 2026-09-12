import { Empty } from "antd";

import { PageContainer } from "./PageContainer";

interface PlaceholderPageProps {
  title: string;
  ticket: string;
}

export function PlaceholderPage({ title, ticket }: PlaceholderPageProps) {
  return (
    <PageContainer title={title}>
      <div className="placeholder-panel">
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={`${title}建设中，将在 ${ticket} 交付`} />
      </div>
    </PageContainer>
  );
}
