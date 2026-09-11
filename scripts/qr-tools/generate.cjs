const { parseArgs, generate } = require('./common.cjs');

const usage = 'Usage: node scripts/qr-tools/generate.cjs [--root PROJECT] [--source SOURCE_ID] [--force]';
if (process.argv.slice(2).includes('--help')) console.log(usage);
else Promise.resolve().then(() => generate(parseArgs(process.argv.slice(2), false), false)).catch(() => {
  console.error('Private QR generation failed. Check setup, source ID and existing output files.');
  console.error(usage);
  process.exitCode = 1;
});
