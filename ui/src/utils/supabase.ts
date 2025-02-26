import { createClient, SupabaseClient } from '@supabase/supabase-js';
import dotenv from 'dotenv';

dotenv.config({ path: '.env' });

const supabaseUrl: string = process.env.SUPABASE_URL || '';
const supabaseKey: string = process.env.SUPABASE_KEY || '';

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

// inserts to the connections table using the record_connection function
export async function addConnection(uuid: string): Promise<void | null> {
  const { data, error } = await supabase
    .rpc('record_connection', { user_id_input: uuid });
  if (error) {
    console.error(error);
  } else {
    console.log('Connection inserted successfully');
    console.log('Data:', data);
    return data;
  }
}

