// Geometry of supplied relationships only; positions never infer new links.
const GOLDEN = Math.PI * (3 - Math.sqrt(5));
const finite = p => p && Number.isFinite(p.x) && Number.isFinite(p.y);
const compare = (a, b) => a < b ? -1 : a > b ? 1 : 0;
const pairKey = (a, b) => JSON.stringify([a, b].sort(compare));

export function graphSignature(nodes, edges) {
  const ids = new Set(nodes.map(n => n.id));
  return JSON.stringify([[...ids].sort(compare), [...new Set(edges.filter(e => ids.has(e.source) && ids.has(e.target) && e.source !== e.target).map(e => pairKey(e.source, e.target)))].sort(compare)]);
}

export function layoutGraph(input, edges, previous = new Map(), {aspect = 1.35, repack = false} = {}) {
  const nodes = [...input].sort((a, b) => compare(a.id, b.id));
  const neighbors = new Map(nodes.map(n => [n.id, new Set()]));
  for (const e of edges) if (e.source !== e.target && neighbors.has(e.source) && neighbors.has(e.target)) {
    neighbors.get(e.source).add(e.target); neighbors.get(e.target).add(e.source);
  }
  const seen = new Set(), groups = [];
  for (const n of nodes) {
    if (seen.has(n.id)) continue;
    const group = [], queue = [n.id]; seen.add(n.id);
    for (let i = 0; i < queue.length; i++) {
      const id = queue[i]; group.push(id);
      for (const other of [...neighbors.get(id)].sort(compare)) if (!seen.has(other)) { seen.add(other); queue.push(other); }
    }
    groups.push(group);
  }
  groups.sort((a, b) => b.length - a.length || compare(a[0], b[0]));
  const placed = [], result = new Map();
  for (const [groupIndex, ids] of groups.entries()) {
    const prior = ids.map(id => previous.get(id)).filter(finite);
    const origin = prior.length ? {x: prior.reduce((v, p) => v + p.x, 0) / prior.length, y: prior.reduce((v, p) => v + p.y, 0) / prior.length} : {x: 0, y: 0};
    const local = new Map(), ordered = [...ids].sort((a, b) => neighbors.get(b).size - neighbors.get(a).size || compare(a, b));
    ordered.forEach((id, i) => {
      const old = previous.get(id), angle = i * GOLDEN + .37, r = 48 * Math.sqrt(i);
      local.set(id, {x: finite(old) ? old.x - origin.x : Math.cos(angle) * r, y: finite(old) ? old.y - origin.y : Math.sin(angle) * r, vx: 0, vy: 0});
    });
    const links = [];
    for (const id of ids) for (const other of neighbors.get(id)) if (compare(id, other) < 0) links.push([id, other]);
    for (let step = 0; step < 420 && ids.length > 1; step++) {
      const forces = new Map(ids.map(id => [id, {x: 0, y: 0}]));
      for (let i = 0; i < ids.length; i++) for (let j = i + 1; j < ids.length; j++) {
        const a = local.get(ids[i]), b = local.get(ids[j]); let dx = b.x - a.x, dy = b.y - a.y;
        if (Math.hypot(dx, dy) < .1) { dx = Math.cos((i + j) * GOLDEN); dy = Math.sin((i + j) * GOLDEN); }
        const d = Math.max(1, Math.hypot(dx, dy)), strength = 22000 / Math.max(35, d) ** 2 + Math.max(0, 92 - d) * .16;
        const fx = dx / d * strength, fy = dy / d * strength;
        forces.get(ids[i]).x -= fx; forces.get(ids[i]).y -= fy;
        forces.get(ids[j]).x += fx; forces.get(ids[j]).y += fy;
      }
      for (const [aID, bID] of links) {
        const a = local.get(aID), b = local.get(bID), dx = b.x - a.x, dy = b.y - a.y, d = Math.max(1, Math.hypot(dx, dy));
        const f = (d - 130) * .028, fx = dx / d * f, fy = dy / d * f;
        forces.get(aID).x += fx; forces.get(aID).y += fy; forces.get(bID).x -= fx; forces.get(bID).y -= fy;
      }
      let movement = 0;
      for (const id of ids) {
        const p = local.get(id), f = forces.get(id);
        p.vx = (p.vx + f.x - p.x * .002) * .68; p.vy = (p.vy + f.y - p.y * .002) * .68;
        const speed = Math.hypot(p.vx, p.vy), limit = Math.min(1, 12 / Math.max(speed, .001));
        p.x += p.vx * limit; p.y += p.vy * limit; movement = Math.max(movement, speed);
      }
      if (step > 120 && movement < .035) break;
    }
    const values = [...local.values()], cx = values.reduce((s, p) => s + p.x, 0) / values.length, cy = values.reduce((s, p) => s + p.y, 0) / values.length;
    for (const p of values) { p.x -= cx; p.y -= cy; }
    const halfW = Math.max(70, ...values.map(p => Math.abs(p.x) + 65)), halfH = Math.max(65, ...values.map(p => Math.abs(p.y) + 45));
    const overlaps = box => placed.some(b => Math.abs(box.x - b.x) < box.w + b.w + 45 && Math.abs(box.y - b.y) < box.h + b.h + 45);
    let box = {x: origin.x, y: origin.y, w: halfW, h: halfH};
    if (!prior.length || overlaps(box) || repack) {
      let best = null, score = Infinity;
      for (let i = 0; i < 480; i++) {
        const angle = i * GOLDEN, r = Math.sqrt(i) * 52;
        const candidate = {x: Math.cos(angle) * r * Math.sqrt(aspect), y: Math.sin(angle) * r / Math.sqrt(aspect), w: halfW, h: halfH};
        if (overlaps(candidate)) continue;
        const all = [...placed, candidate], w = Math.max(...all.map(b => b.x + b.w)) - Math.min(...all.map(b => b.x - b.w)), h = Math.max(...all.map(b => b.y + b.h)) - Math.min(...all.map(b => b.y - b.h));
        const cost = w * h * (1 + Math.abs(Math.log(w / h / aspect))) + r * 20;
        if (cost < score) { score = cost; best = candidate; }
        if (i > 100 && best) break;
      }
      box = best || {x: placed.reduce((m, b) => Math.max(m, b.x + b.w), 0) + halfW + 60, y: 0, w: halfW, h: halfH};
    }
    placed.push(box);
    for (const [id, p] of local) result.set(id, {x: p.x + box.x, y: p.y + box.y, group: groupIndex});
  }
  return result;
}

export function fitGraph(points, width = 1000, height = 660) {
  if (!points.size) return {x: width / 2, y: height / 2, k: 1};
  const all = [...points.values()], minX = Math.min(...all.map(p => p.x)), maxX = Math.max(...all.map(p => p.x)), minY = Math.min(...all.map(p => p.y)), maxY = Math.max(...all.map(p => p.y));
  const k = Math.max(.12, Math.min(1.35, Math.max(80, width - (width < 560 ? 120 : 190)) / Math.max(150, maxX - minX), Math.max(100, height - 130) / Math.max(150, maxY - minY)));
  return {k, x: width / 2 - (minX + maxX) / 2 * k, y: height / 2 - (minY + maxY) / 2 * k};
}

// Screen-space labels retain their slot where possible instead of flickering
// between sides on each wheel tick. Selected and hovered labels are placed first.
export function placeLabels(items, width, height, slots = new Map()) {
  const boxes = [], placements = new Map(), options = [[0, 24, 'middle'], [0, -18, 'middle'], [16, 4, 'start'], [-16, 4, 'end']];
  for (const item of [...items].sort((a, b) => b.priority - a.priority || compare(a.id, b.id))) {
    const choices = [slots.get(item.id), 0, 1, 2, 3].filter((v, i, all) => v != null && all.indexOf(v) === i);
    for (const slot of choices) {
      const [dx, dy, anchor] = options[slot], left = item.x + dx - (anchor === 'middle' ? item.width / 2 : anchor === 'end' ? item.width : 0), top = item.y + dy - 12;
      const box = {left: left - 4, right: left + item.width + 4, top: top - 3, bottom: top + 19};
      if (box.left < 8 || box.right > width - 8 || box.top < 8 || box.bottom > height - 8) continue;
      if (boxes.some(b => box.left < b.right && box.right > b.left && box.top < b.bottom && box.bottom > b.top)) continue;
      if (items.some(n => n.id !== item.id && n.x > box.left - 9 && n.x < box.right + 9 && n.y > box.top - 9 && n.y < box.bottom + 9)) continue;
      placements.set(item.id, {dx, dy, anchor, slot, box}); boxes.push(box); break;
    }
  }
  return placements;
}
