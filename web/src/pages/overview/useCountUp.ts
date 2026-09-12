import { useEffect, useRef, useState } from "react";

export function useCountUp(target: number, duration = 700): number {
  const [value, setValue] = useState(0);
  const current = useRef(0);

  useEffect(() => {
    const safeTarget = Number.isFinite(target) ? Math.max(0, target) : 0;
    const startValue = Number.isFinite(current.current) ? current.current : 0;
    const startedAt = performance.now();
    let frame = 0;

    const tick = (now: number) => {
      const progress = Math.min(1, Math.max(0, (now - startedAt) / duration));
      const eased = 1 - (1 - progress) ** 3;
      const next = startValue + (safeTarget - startValue) * eased;
      current.current = next;
      setValue(next);
      if (progress < 1) {
        frame = requestAnimationFrame(tick);
      } else {
        current.current = safeTarget;
        setValue(safeTarget);
      }
    };

    frame = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(frame);
  }, [duration, target]);

  return Number.isFinite(value) ? value : 0;
}
