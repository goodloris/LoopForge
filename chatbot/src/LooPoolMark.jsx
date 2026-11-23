import React, { useId } from "react";

// The LooPool ring, static, for the sidebar rail brand. Same artwork as the home
// logo (web/loopool-mark.svg); a logo keeps its own baked colours in both themes.
const SEEDS = [
  [14.25, 42.25, 255], [15.1, 27.47, 300], [22, 14.68, 345], [28.61, 51.2, 205],
  [41.46, 45.52, 160], [51.8, 37.31, 120], [33.31, 17.06, 20], [18.21, 34.43, 275],
  [34.43, 45.79, 185], [44.8, 38.81, 133], [20.94, 24.26, 320], [19.38, 50.02, 230],
  [27.43, 10.48, 3],
];

export default function LooPoolMark({ size = 25 }) {
  const identity = useId().replaceAll(":", "");
  const crust = `bgl-crust-${identity}`;
  const tip = `bgl-tip-${identity}`;
  const hole = `bgl-hole-${identity}`;
  return (
    <svg
      className="loopool-mark"
      viewBox="0 0 64 64"
      width={size}
      height={size}
      fill="none"
      role="img"
      aria-label="LooPool"
      focusable="false"
    >
      <defs>
        <linearGradient id={crust} x1="12" y1="10" x2="54" y2="56" gradientUnits="userSpaceOnUse">
          <stop offset="0" stopColor="#FFD592" />
          <stop offset=".5" stopColor="#F0A043" />
          <stop offset="1" stopColor="#B4561C" />
        </linearGradient>
        <linearGradient id={tip} x1="34" y1="4" x2="48" y2="22" gradientUnits="userSpaceOnUse">
          <stop offset="0" stopColor="#FFC774" />
          <stop offset="1" stopColor="#D9782A" />
        </linearGradient>
        <radialGradient id={hole} cx="32" cy="32" r="11.2" gradientUnits="userSpaceOnUse">
          <stop offset=".5" stopColor="#6B300D" stopOpacity="0" />
          <stop offset="1" stopColor="#6B300D" stopOpacity=".4" />
        </radialGradient>
      </defs>
      <path d="M46.92 21.93 A18 18 0 1 1 35.13 14.27" fill="none" stroke={`url(#${crust})`} strokeWidth="14" />
      <path d="M9.59 26 A23.2 23.2 0 0 1 24.07 10.2" fill="none" stroke="#FFF1D2" strokeOpacity=".8" strokeWidth="2.2" strokeLinecap="round" />
      <circle cx="32" cy="32" r="11.2" fill={`url(#${hole})`} />
      <g fill="#FFF6E2">
        {SEEDS.map(([cx, cy, r]) => (
          <ellipse key={`${cx}-${cy}`} cx={cx} cy={cy} rx="1.15" ry="2.1" transform={`rotate(${r} ${cx} ${cy})`} />
        ))}
      </g>
      <path d="M37.04 3.44 L44.34 17.29 L33.81 21.76 Z" fill={`url(#${tip})`} stroke="#B4561C" strokeWidth=".8" strokeLinejoin="round" />
    </svg>
  );
}
