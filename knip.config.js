import { defineConfig } from "./.datamitsu/knip.config.js";

export default defineConfig((config) => ({
  ...config,
  entry: [...config.entry, "stylelint.config.mjs"],
  ignoreBinaries: [...config.ignoreBinaries, "go"],
}));
