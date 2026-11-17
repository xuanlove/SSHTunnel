<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import * as api from '@/api'
import type { CityProvince } from '@/api'

interface CountryOption {
  label: string
  value: string
}

interface RuleRow {
  country: string
  province: string
  city: string
}

const props = defineProps<{
  modelValue: string[]
  countryOptions: CountryOption[]
  cityReady: boolean
}>()

const emit = defineEmits<{ 'update:modelValue': [string[]] }>()

const ANY = '*'

function parseRules(rules: string[]): RuleRow[] {
  return (rules || []).map((raw) => {
    const parts = String(raw).split('/').map((s) => s.trim())
    if (parts.length === 1) {
      return { country: parts[0] || ANY, province: ANY, city: ANY }
    }
    return {
      country: parts[0] || ANY,
      province: parts[1] || ANY,
      city: parts[2] || ANY,
    }
  })
}

function formatRule(r: RuleRow): string {
  return `${r.country || ANY}/${r.province || ANY}/${r.city || ANY}`
}

const rows = ref<RuleRow[]>(parseRules(props.modelValue))
let lastEmitted = JSON.stringify(props.modelValue || [])

watch(
  () => props.modelValue,
  (val) => {
    if (JSON.stringify(val || []) === lastEmitted) return
    rows.value = parseRules(val || [])
    rows.value.forEach((r) => void loadCatalog(r.country))
  },
  { deep: true },
)

function flush() {
  const next = rows.value.filter((r) => r.country).map(formatRule)
  lastEmitted = JSON.stringify(next)
  emit('update:modelValue', next)
}

function addRow() {
  rows.value = [...rows.value, { country: 'CN', province: ANY, city: ANY }]
  void loadCatalog('CN')
  flush()
}

function removeRow(idx: number) {
  const arr = [...rows.value]
  arr.splice(idx, 1)
  rows.value = arr
  flush()
}

function onCountryChange(row: RuleRow) {
  row.province = ANY
  row.city = ANY
  void loadCatalog(row.country)
  flush()
}

function onProvinceChange(row: RuleRow) {
  row.city = ANY
  flush()
}

// ===== 城市目录（按国家懒加载，服务端后台构建索引） =====
const catalogs = reactive<Record<string, CityProvince[]>>({})
const loading = reactive<Record<string, boolean>>({})
const retryTimers = new Map<string, number>()

async function loadCatalog(country: string, attempt = 0) {
  if (!country || country === ANY || catalogs[country] || loading[country]) return
  loading[country] = true
  try {
    const res = await api.getCityCatalog(country)
    if (res.ready) {
      catalogs[country] = res.provinces || []
    } else if (res.building && attempt < 20) {
      // 索引仍在构建：2 秒后重试
      retryTimers.set(
        country,
        window.setTimeout(() => {
          retryTimers.delete(country)
          loading[country] = false
          void loadCatalog(country, attempt + 1)
        }, 2000),
      )
    }
  } catch {
    // 忽略：无下拉选项时仍可手动输入
  } finally {
    if (!retryTimers.has(country)) loading[country] = false
  }
}

function provinceOptions(country: string): CityProvince[] {
  return catalogs[country] || []
}

function findProvince(row: RuleRow): CityProvince | undefined {
  const list = catalogs[row.country] || []
  return list.find((p) => (p.code && p.code === row.province) || p.name === row.province)
}

function cityOptions(row: RuleRow): string[] {
  const list = catalogs[row.country]
  if (!list) return []
  if (row.province && row.province !== ANY) {
    return findProvince(row)?.cities || []
  }
  const set = new Set<string>()
  list.forEach((p) => p.cities.forEach((c) => set.add(c)))
  return Array.from(set).sort()
}

const loadingHint = computed(() =>
  rows.value.some((r) => loading[r.country] && !catalogs[r.country]),
)

// 初始化：加载已有规则涉及的国家
const initial = new Set(rows.value.map((r) => r.country).filter((c) => c && c !== ANY))
initial.forEach((c) => void loadCatalog(c))
</script>

<template>
  <div class="city-editor">
    <div v-for="(row, idx) in rows" :key="idx" class="rule-row">
      <el-select
        :model-value="row.country"
        filterable
        allow-create
        default-first-option
        placeholder="国家"
        style="width: 140px"
        @update:model-value="(v: string) => { row.country = v; onCountryChange(row) }"
      >
        <el-option v-for="c in countryOptions" :key="c.value" :label="c.label" :value="c.value" />
      </el-select>
      <el-select
        :model-value="row.province"
        filterable
        allow-create
        default-first-option
        placeholder="省/州"
        style="width: 190px"
        @update:model-value="(v: string) => { row.province = v; onProvinceChange(row) }"
      >
        <el-option label="全部（*）" :value="ANY" />
        <el-option
          v-for="p in provinceOptions(row.country)"
          :key="p.name"
          :label="p.code ? `${p.name}（${p.code}）` : p.name"
          :value="p.code || p.name"
        />
      </el-select>
      <el-select
        :model-value="row.city"
        filterable
        allow-create
        default-first-option
        placeholder="城市"
        style="width: 190px"
        @update:model-value="(v: string) => { row.city = v; flush() }"
      >
        <el-option label="全部（*）" :value="ANY" />
        <el-option v-for="c in cityOptions(row)" :key="c" :label="c" :value="c" />
      </el-select>
      <el-button type="danger" size="small" link @click="removeRow(idx)">删除</el-button>
    </div>
    <el-button class="add-btn" size="small" @click="addRow">+ 添加城市规则</el-button>
    <div v-if="!cityReady" class="fw-tip">
      城市下拉选项需要城市级 GeoIP 数据库：请到「系统设置 → GeoIP 城市级数据库」下载并启用；下载前仍可手动输入。
    </div>
    <div v-else-if="loadingHint" class="fw-hint">
      正在构建城市列表（首次约需数十秒），可先手动输入。
    </div>
    <div v-else class="fw-hint">
      选择国家/省/城市即生成「国家/省/城市」规则；选「全部（*）」表示通配。
    </div>
  </div>
</template>

<style scoped>
.city-editor {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
.rule-row {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.add-btn {
  border-style: dashed;
  width: 100%;
}
.fw-tip {
  font-size: 12px;
  color: #ebb563;
  line-height: 1.5;
}
.fw-hint {
  font-size: 12px;
  color: #8a8d95;
  line-height: 1.5;
}
</style>