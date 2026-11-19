<script setup lang="ts">
import { reactive, computed, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { useTunnelStore } from '@/stores/tunnel'
import * as api from '@/api'
import type { TunnelConfig, LocalForward, ProxyListener } from '@/types'
import HopEditor from '@/components/HopEditor.vue'
import ProxyListenerEditor from '@/components/ProxyListenerEditor.vue'

const route = useRoute()
const router = useRouter()
const store = useTunnelStore()

const form = reactive<TunnelConfig>(emptyForm())

function emptyForm(): TunnelConfig {
  return {
    id: '',
    name: '',
    hop_chain: [],
    tunnel_type: 'proxy',
    local_forwards: [],
    proxy_listeners: [],
    auto_reconnect: false,
    auto_start: false,
    status: 'stopped',
    keepalive_seconds: 60,
    max_reconnect_attempts: 0,
    reconnect_max_delay_seconds: 60,
  }
}

// 包装 computed 解决 reactive 中可选数组 undefined 的类型问题
const proxyListeners = computed<ProxyListener[]>({
  get: () => form.proxy_listeners ?? [],
  set: (v) => { form.proxy_listeners = v },
})

const isEdit = computed(() => !!route.params.id)

function addLocalForward() {
  form.local_forwards = [...(form.local_forwards || []), {
    id: `lf-${Date.now()}`,
    local_port: 8080,
    remote_host: '',
    remote_port: 80,
    allow_external: false,
  }]
}

function removeLocalForward(idx: number) {
  const arr = [...(form.local_forwards || [])]
  arr.splice(idx, 1)
  form.local_forwards = arr
}

function updateLocalForward(idx: number, patch: Partial<LocalForward>) {
  const arr = [...(form.local_forwards || [])]
  arr[idx] = { ...arr[idx], ...patch }
  form.local_forwards = arr
}

async function handleTest() {
  try {
    await api.testTunnelConfig({ ...form })
    ElMessage.success('连接测试成功')
  } catch (e: any) {
    ElMessage.error(e?.message || '测试失败')
  }
}

async function handleSave() {
  if (!form.name.trim()) {
    ElMessage.warning('请填写隧道名称')
    return
  }
  if (!form.hop_chain.length) {
    ElMessage.warning('请配置至少一跳')
    return
  }
  try {
    await store.save({ ...form })
    ElMessage.success('保存成功')
    router.push('/')
  } catch (e: any) {
    ElMessage.error(e?.message || '保存失败')
  }
}

async function initForm() {
  // 路由复用（编辑 -> 新建）时重置表单，避免覆盖原隧道
  Object.assign(form, emptyForm())
  if (!route.params.id) return
  if (!store.configs.length) await store.loadConfigs()
  const cfg = store.configs.find(c => c.id === route.params.id)
  if (!cfg) {
    ElMessage.error('配置不存在')
    router.push('/')
    return
  }
  const parsed = JSON.parse(JSON.stringify(cfg))
  Object.assign(form, parsed)
  // 旧配置没有心跳字段时保持关闭，避免仅编辑保存就静默开启心跳
  if (parsed.keepalive_seconds === undefined) form.keepalive_seconds = 0
  if (parsed.max_reconnect_attempts === undefined) form.max_reconnect_attempts = 0
  if (parsed.reconnect_max_delay_seconds === undefined) form.reconnect_max_delay_seconds = 60
}

// 监听路由参数：同一组件在「编辑」与「新建」之间切换时重新初始化
watch(() => route.params.id, initForm, { immediate: true })
</script>

<template>
  <div class="config-edit">
    <div class="toolbar">
      <el-button @click="router.push('/')">← 返回</el-button>
      <h2>{{ isEdit ? '编辑隧道' : '新建隧道' }}</h2>
    </div>

    <el-form label-width="100px" class="form">
      <el-form-item label="隧道名称">
        <el-input v-model="form.name" placeholder="例如：公司内网" />
      </el-form-item>

      <el-form-item label="隧道类型">
        <el-radio-group v-model="form.tunnel_type">
          <el-radio value="local_forward">本地端口转发</el-radio>
          <el-radio value="proxy">代理服务</el-radio>
        </el-radio-group>
      </el-form-item>

      <el-form-item label="跳板链">
        <HopEditor v-model="form.hop_chain" />
      </el-form-item>

      <el-form-item label="自动重连">
        <el-switch v-model="form.auto_reconnect" />
      </el-form-item>

      <el-form-item label="SSH 心跳">
        <el-input-number
          v-model="form.keepalive_seconds"
          :min="0"
          :max="3600"
          :step="10"
          controls-position="right"
          style="width: 140px"
        />
        <span class="form-hint">间隔秒数，0 表示关闭（建议 60），用于及时发现僵死连接</span>
      </el-form-item>

      <el-form-item label="重连策略">
        <div class="reconnect-row">
          <span class="row-label">最大次数</span>
          <el-input-number
            v-model="form.max_reconnect_attempts"
            :min="0"
            :max="1000"
            controls-position="right"
            style="width: 130px"
          />
          <span class="form-hint">0 表示无限</span>
          <span class="row-label">最大间隔</span>
          <el-input-number
            v-model="form.reconnect_max_delay_seconds"
            :min="0"
            :max="3600"
            :step="10"
            controls-position="right"
            style="width: 130px"
          />
          <span class="form-hint">秒，0 表示默认 60</span>
        </div>
      </el-form-item>

      <el-form-item label="开机启动">
        <el-switch v-model="form.auto_start" />
        <span class="form-hint">程序/服务启动时自动启动该隧道（配合 systemd 服务即开机自启）</span>
      </el-form-item>

      <el-form-item v-if="form.tunnel_type === 'local_forward'" label="本地转发">
        <div class="forward-list">
          <div v-for="(lf, idx) in form.local_forwards" :key="lf.id" class="forward-row">
            <el-input-number v-model="lf.local_port" :min="1" :max="65535" controls-position="right" placeholder="本地端口" />
            <el-input v-model="lf.remote_host" placeholder="远程主机" style="flex: 1" />
            <el-input-number v-model="lf.remote_port" :min="1" :max="65535" controls-position="right" placeholder="远程端口" />
            <el-switch v-model="lf.allow_external" active-text="外部访问" />
            <el-button type="danger" size="small" @click="removeLocalForward(idx)">删除</el-button>
          </div>
          <el-button class="add-btn" @click="addLocalForward">+ 添加转发</el-button>
        </div>
      </el-form-item>

      <el-form-item v-if="form.tunnel_type === 'proxy'" label="代理监听">
        <ProxyListenerEditor v-model="proxyListeners" />
      </el-form-item>

      <el-form-item>
        <el-button @click="handleTest">测试连接</el-button>
        <el-button type="primary" @click="handleSave">保存</el-button>
        <el-button @click="router.push('/')">取消</el-button>
      </el-form-item>
    </el-form>
  </div>
</template>

<style scoped>
.config-edit {
  display: flex;
  flex-direction: column;
  gap: 16px;
}
.toolbar {
  display: flex;
  align-items: center;
  gap: 12px;
}
.toolbar h2 {
  font-size: 18px;
  color: #f5f6f7;
}
.form {
  background-color: #252830;
  border: 1px solid #3a3f4b;
  border-radius: 4px;
  padding: 20px;
}
.forward-list {
  display: flex;
  flex-direction: column;
  gap: 10px;
  width: 100%;
}
.forward-row {
  display: flex;
  align-items: center;
  gap: 8px;
  background-color: #14161a;
  padding: 8px;
  border-radius: 4px;
  border: 1px solid #3a3f4b;
}
.add-btn {
  border-style: dashed;
  width: 100%;
}
.form-hint {
  margin-left: 12px;
  font-size: 12px;
  color: #8a8d95;
}
.reconnect-row {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.row-label {
  font-size: 13px;
  color: #c0c3cb;
}
</style>
