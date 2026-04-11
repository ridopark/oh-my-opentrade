"use client";

import { useEffect, useRef } from "react";

type Candle = {
  x: number;
  open: number;
  close: number;
  high: number;
  low: number;
};

export function CandlestickCanvas() {
  const canvasRef = useRef<HTMLCanvasElement | null>(null);

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const ctx = canvas.getContext("2d");
    if (!ctx) return;

    let raf = 0;
    let width = 0;
    let height = 0;
    let candles: Candle[] = [];
    const candleWidth = 6;
    const gap = 4;
    const step = candleWidth + gap;
    let offset = 0;
    let lastPrice = 100;

    const resize = () => {
      const dpr = window.devicePixelRatio || 1;
      width = canvas.clientWidth;
      height = canvas.clientHeight;
      canvas.width = Math.floor(width * dpr);
      canvas.height = Math.floor(height * dpr);
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      candles = [];
      const needed = Math.ceil(width / step) + 4;
      for (let i = 0; i < needed; i++) {
        candles.push(nextCandle(i * step));
      }
    };

    const nextCandle = (x: number): Candle => {
      const drift = (Math.random() - 0.5) * 2;
      const open = lastPrice;
      const close = Math.max(20, Math.min(180, open + drift));
      const high = Math.max(open, close) + Math.random() * 1.2;
      const low = Math.min(open, close) - Math.random() * 1.2;
      lastPrice = close;
      return { x, open, close, high, low };
    };

    const priceToY = (p: number) => {
      const padTop = 40;
      const padBot = 60;
      const usable = height - padTop - padBot;
      const min = 20;
      const max = 180;
      return padTop + (1 - (p - min) / (max - min)) * usable;
    };

    const render = () => {
      ctx.clearRect(0, 0, width, height);
      offset -= 0.25;
      if (offset <= -step) {
        offset += step;
        candles.shift();
        const lastX = candles.length > 0 ? candles[candles.length - 1].x : 0;
        candles.push(nextCandle(lastX + step));
      }

      for (const c of candles) {
        const x = c.x + offset;
        if (x < -step || x > width + step) continue;
        const isUp = c.close >= c.open;
        const color = isUp ? "rgba(240,240,250,0.18)" : "rgba(240,240,250,0.10)";
        ctx.strokeStyle = color;
        ctx.fillStyle = color;
        ctx.lineWidth = 1;
        ctx.beginPath();
        ctx.moveTo(x + candleWidth / 2, priceToY(c.high));
        ctx.lineTo(x + candleWidth / 2, priceToY(c.low));
        ctx.stroke();
        const bodyTop = priceToY(Math.max(c.open, c.close));
        const bodyBot = priceToY(Math.min(c.open, c.close));
        ctx.fillRect(x, bodyTop, candleWidth, Math.max(1, bodyBot - bodyTop));
      }

      raf = requestAnimationFrame(render);
    };

    resize();
    window.addEventListener("resize", resize);
    raf = requestAnimationFrame(render);
    return () => {
      cancelAnimationFrame(raf);
      window.removeEventListener("resize", resize);
    };
  }, []);

  return (
    <canvas
      ref={canvasRef}
      aria-hidden="true"
      className="absolute inset-0 h-full w-full pointer-events-none"
    />
  );
}
