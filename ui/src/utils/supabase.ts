import { createClient, SupabaseClient } from '@supabase/supabase-js';
import dotenv from 'dotenv';

dotenv.config({ path: '.env' });

const supabaseUrl: string = process.env.SUPABASE_URL || '';
const supabaseKey: string = process.env.SUPABASE_KEY || '';
const supabaseTestUUID: string = process.env.SUPABASE_TEST_UUID || '';

if (!supabaseUrl || !supabaseKey) {
  throw new Error('Missing Supabase URL or Key in environment variables');
}

const supabase: SupabaseClient = createClient(supabaseUrl, supabaseKey);

export async function testRequest(): Promise<void> {
  console.log(`PLACEHOLDER for supabaseRequest to ${supabaseUrl}`);
}

// lists all rows in connections table
export async function fetchConnections(): Promise<any[] | null> {
  const { data: connections, error } = await supabase
    .from('connections')
    .select('*');
  if (error) {
    console.error('Error fetching connections:', error);
    return null;
  } else {
    return connections;
  }
}

interface AddConnectionResult {
  success: boolean;
  data?: any[];
  error?: string;
}

export async function addConnection(uuid: string): Promise<AddConnectionResult> {
  try {
    const { data, error } = await supabase
      .rpc('record_connection', { user_id_input: uuid });

    if (error) {
      console.error("record_connection failed: ", error);
      return { success: false, error: error.message };
    }

    console.log('Connection inserted successfully: ', data);
    return { success: true, data };
  } catch (err) {
    console.error('Unexpected error: ', err);
    if (err instanceof Error) {
      return { success: false, error: err.message };
    } else {
      return { success: false, error: 'An unknown error occurred' };
    }
  }
}