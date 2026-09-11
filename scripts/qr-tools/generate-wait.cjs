const { parseArgs, generate } = require('./common.cjs');

const usage = 'Usage: node scripts/qr-tools/generate-wait.cjs SOURCE_ID 80|120|300 [--root PROJECT] [--force]';
if (process.argv.slice(2).includes('--help')) console.log(usage);
else Promise.resolve().then(() => generate(parseArgs(process.argv.slice(2), true), true)).catch(() => {
  console.error('Private QR generation failed. Check setup, source ID and existing output files.');
  console.error(usage);
  process.exitCode = 1;
});
