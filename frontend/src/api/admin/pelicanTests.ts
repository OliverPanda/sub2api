import { apiClient } from '../client'
import type {
  CreatePelicanTestRunRequest,
  PelicanTestRun,
  PelicanTestRunDetail
} from '@/types'

export async function listRuns(params?: {
  group_id?: number
  limit?: number
}): Promise<PelicanTestRun[]> {
  const { data } = await apiClient.get<PelicanTestRun[]>('/admin/pelican-tests/runs', {
    params
  })
  return data ?? []
}

export async function createRun(req: CreatePelicanTestRunRequest): Promise<PelicanTestRun> {
  const { data } = await apiClient.post<PelicanTestRun>('/admin/pelican-tests/runs', req)
  return data
}

export async function getRun(id: number): Promise<PelicanTestRunDetail> {
  const { data } = await apiClient.get<PelicanTestRunDetail>(`/admin/pelican-tests/runs/${id}`)
  return data
}

export async function deleteRun(id: number): Promise<void> {
  await apiClient.delete(`/admin/pelican-tests/runs/${id}`)
}

export const pelicanTestsAPI = {
  listRuns,
  createRun,
  getRun,
  deleteRun
}

export default pelicanTestsAPI
