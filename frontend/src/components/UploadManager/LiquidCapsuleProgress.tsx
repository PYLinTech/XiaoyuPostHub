import React, { CSSProperties, useMemo } from 'react';
import styles from './index.module.less';

export interface LiquidCapsuleProgressProps {
  progress: number;
  width?: number;
  height?: number;
  liquidColor?: string;
  backgroundColor?: string;
  waveAmplitude?: number;
  animationDuration?: number;
  className?: string;
  ariaLabel?: string;
  onClick?: () => void;
}

function clamp(value: number, minimum: number, maximum: number) {
  return Math.min(maximum, Math.max(minimum, value));
}

function wavePath(
  width: number,
  height: number,
  amplitude: number,
  period: number
) {
  const top = -height * 2;
  const bottom = height * 3;
  const left = -width - amplitude * 2;
  const parts = [`M ${left} ${top}`, `L 0 ${top}`];
  for (let y = top; y < bottom; y += period) {
    parts.push(
      `C ${amplitude} ${y + period * 0.25}, ${-amplitude} ${
        y + period * 0.75
      }, 0 ${y + period}`
    );
  }
  parts.push(`L ${left} ${bottom}`, 'Z');
  return parts.join(' ');
}

export default function LiquidCapsuleProgress({
  progress,
  width = 94,
  height = 40,
  liquidColor = 'rgb(var(--primary-4))',
  backgroundColor = 'var(--color-bg-2)',
  waveAmplitude = 3,
  animationDuration = 2.4,
  className,
  ariaLabel,
  onClick,
}: LiquidCapsuleProgressProps) {
  const value = clamp(Number.isFinite(progress) ? progress : 0, 0, 100);
  const amplitude = clamp(waveAmplitude, 0.5, height / 4);
  const period = Math.max(8, height / 2);
  const liquidX = (width * value) / 100;
  const primaryWave = useMemo(
    () => wavePath(width, height, amplitude, period),
    [amplitude, height, period, width]
  );
  const secondaryWave = useMemo(
    () => wavePath(width, height, amplitude * 0.65, period),
    [amplitude, height, period, width]
  );
  const hidden = value <= 0;
  const complete = value >= 100;
  const rootStyle = {
    '--liquid-width': `${width}px`,
    '--liquid-height': `${height}px`,
    '--liquid-background': backgroundColor,
    '--liquid-color': liquidColor,
    '--liquid-x': `${liquidX}px`,
    '--wave-period': `${period}px`,
    '--wave-duration': `${Math.max(0.6, animationDuration)}s`,
    '--wave-duration-secondary': `${Math.max(0.8, animationDuration * 1.35)}s`,
  } as CSSProperties;

  return (
    <button
      type="button"
      className={[styles.capsule, className].filter(Boolean).join(' ')}
      style={rootStyle}
      aria-label={ariaLabel}
      onClick={onClick}
    >
      <svg
        className={styles['liquid-canvas']}
        width={width}
        height={height}
        viewBox={`0 0 ${width} ${height}`}
        aria-hidden="true"
      >
        {!hidden && !complete && (
          <>
            <g className={styles['wave-position']}>
              <path
                className={`${styles.wave} ${styles['wave-secondary']}`}
                d={secondaryWave}
              />
            </g>
            <g className={styles['wave-position']}>
              <path className={styles.wave} d={primaryWave} />
            </g>
          </>
        )}
        {complete && (
          <rect
            className={styles['liquid-complete']}
            width={width}
            height={height}
          />
        )}
      </svg>
      <span className={styles['capsule-label']}>{Math.round(value)}%</span>
    </button>
  );
}
