import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Derleme çıktısı Go programına gömülür (internal/panelui). public/.gitkeep çıktıya kopyalanır;
// böylece klasör boşaltılsa da depodaki yer tutucu yerinde kalır.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "../internal/panelui/dist",
    emptyOutDir: true,
  },
  server: {
    // Geliştirirken API istekleri çalışan panele aktarılır.
    proxy: { "/api": "http://localhost:8002" },
  },
});
