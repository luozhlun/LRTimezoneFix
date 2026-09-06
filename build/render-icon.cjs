// Run with Node.js and sharp installed: node build/render-icon.cjs
// Optional SHARP_MODULE lets the build use an existing toolchain installation.
const path = require('node:path');
const sharp = require(process.env.SHARP_MODULE || 'sharp');
sharp(path.join(__dirname, '../frontend/public/appicon.svg'))
  .resize(512, 512).png().toFile(path.join(__dirname, 'appicon.png'))
  .catch(error => { console.error(error); process.exitCode = 1; });
