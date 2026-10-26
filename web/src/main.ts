import { createApp } from 'vue'
import { createRouter, createWebHistory } from 'vue-router'
import App from './App.vue'
import './style.css'

import OverviewPage from './pages/OverviewPage.vue'
import FirewallPage from './pages/FirewallPage.vue'
import NetworkPage from './pages/NetworkPage.vue'
import AuditPage from './pages/AuditPage.vue'
import SettingsPage from './pages/SettingsPage.vue'
import LoginPage from './pages/LoginPage.vue'

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', name: 'overview', component: OverviewPage, meta: { title: '概览' } },
    { path: '/firewall', name: 'firewall', component: FirewallPage, meta: { title: 'IPv6 防火墙' } },
    { path: '/network', name: 'network', component: NetworkPage, meta: { title: '网络诊断' } },
    { path: '/audit', name: 'audit', component: AuditPage, meta: { title: '操作审计' } },
    { path: '/settings', name: 'settings', component: SettingsPage, meta: { title: '设置' } },
    { path: '/login', name: 'login', component: LoginPage, meta: { title: '登录', public: true } },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
  scrollBehavior() {
    return { top: 0 }
  },
})

router.afterEach((to) => {
  const title = (to.meta.title as string | undefined) ?? ''
  document.title = title ? `${title} · HomeLab 管理面板` : 'HomeLab 管理面板'
})

createApp(App).use(router).mount('#app')
