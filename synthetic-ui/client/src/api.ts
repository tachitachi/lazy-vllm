import axios from 'axios';

const API_BASE_URL = import.meta.env.VITE_API_URL || 'http://localhost:3003/api';

export type Message = {
  role: 'user' | 'assistant';
  content: string;
};

export type Thread = {
  thread_id: string;
  history: Message[];
  labels: string[];
};

export const api = {
  getThreads: async (): Promise<string[]> => {
    const response = await axios.get(`${API_BASE_URL}/threads`);
    return response.data;
  },
  getThread: async (id: string): Promise<Thread> => {
    const response = await axios.get(`${API_BASE_URL}/threads/${id}`);
    return response.data;
  },
  updateLabel: async (id: string, labels: string[]): Promise<void> => {
    await axios.post(`${API_BASE_URL}/threads/${id}/label`, { labels });
  },
  archiveThread: async (id: string): Promise<void> => {
    await axios.post(`${API_BASE_URL}/threads/${id}/archive`);
  },
};
